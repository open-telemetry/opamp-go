package signing

import (
	"context"
	"crypto"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// policyServer is a fake /v1/sign, /v1/chain, /v1/ca signing service.
type policyServer struct {
	t      *testing.T
	root   *x509.Certificate
	rootKy crypto.Signer

	mu          sync.Mutex
	signKey     crypto.Signer
	chainPEM    []byte
	signStatus  int
	chainStatus int
	caStatus    int
	chainGate   func() // runs before each /v1/chain response, outside mu
	// broken maps a path to "hangup" (close without responding) or
	// "truncate" (promise more body than is sent).
	broken map[string]string

	signs        atomic.Int32
	chainFetches atomic.Int32
	srv          *httptest.Server
}

func newPolicyServer(t *testing.T) *policyServer {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	p := &policyServer{t: t, root: root, rootKy: rootKey}
	p.rotate()
	p.srv = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.srv.Close)
	return p
}

// rotate issues a new signing leaf and serves its chain from now on.
func (p *policyServer) rotate() {
	leaf, key := newTestLeaf(p.t, AlgorithmECDSAP256SHA256, p.root, p.rootKy)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signKey = key
	p.chainPEM = pemCert(leaf)
}

func (p *policyServer) handle(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	mode := p.broken[r.URL.Path]
	p.mu.Unlock()
	if mode != "" {
		conn, _, err := w.(http.Hijacker).Hijack()
		if !assert.NoError(p.t, err) {
			return
		}
		if mode == "truncate" {
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort"))
		}
		_ = conn.Close()
		return
	}
	switch r.URL.Path {
	case "/v1/sign":
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		status, key := p.signStatus, p.signKey
		p.mu.Unlock()
		p.signs.Add(1)
		if status != 0 {
			http.Error(w, "sign failed", status)
			return
		}
		sig, err := signWithKey(key, AlgorithmECDSAP256SHA256, body)
		assert.NoError(p.t, err)
		_, _ = w.Write(sig)
	case "/v1/chain":
		p.chainFetches.Add(1)
		p.mu.Lock()
		gate := p.chainGate
		p.mu.Unlock()
		if gate != nil {
			gate()
		}
		p.mu.Lock()
		status, body := p.chainStatus, p.chainPEM
		p.mu.Unlock()
		if status != 0 {
			http.Error(w, "chain failed", status)
			return
		}
		_, _ = w.Write(body)
	case "/v1/ca":
		p.mu.Lock()
		status := p.caStatus
		p.mu.Unlock()
		if status != 0 {
			http.Error(w, "ca failed", status)
			return
		}
		_, _ = w.Write(pemCert(p.root))
	default:
		http.NotFound(w, r)
	}
}

func (p *policyServer) set(f func(p *policyServer)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// assertVerifies checks res the way an Agent would.
func (p *policyServer) assertVerifies(t *testing.T, res SignResult) {
	t.Helper()
	v, err := NewLocalVerifier(rootPool(p.root))
	require.NoError(t, err)
	vc, err := v.ValidateChain(t.Context(), res.ChainDER, time.Now(), testHost)
	require.NoError(t, err)
	require.NoError(t, v.Verify(t.Context(), res.Payload, res.Signature, vc))
}

func TestRemoteSignerSign(t *testing.T) {
	p := newPolicyServer(t)
	rs := NewRemoteSigner(p.srv.URL + "/")

	payload := []byte("payload")
	res, err := rs.Sign(t.Context(), payload)
	require.NoError(t, err)
	assert.Equal(t, payload, res.Payload)
	p.assertVerifies(t, res)

	// Within the TTL the cached chain is reused.
	_, err = rs.Sign(t.Context(), payload)
	require.NoError(t, err)
	assert.EqualValues(t, 1, p.chainFetches.Load())
}

func TestRemoteSignerCacheDisabled(t *testing.T) {
	p := newPolicyServer(t)
	rs := NewRemoteSigner(p.srv.URL)
	rs.SetChainCacheTTL(0)

	for i := 0; i < 3; i++ {
		_, err := rs.Sign(t.Context(), []byte("p"))
		require.NoError(t, err)
	}
	assert.EqualValues(t, 3, p.chainFetches.Load())
}

func TestRemoteSignerErrors(t *testing.T) {
	cases := map[string]func(p *policyServer){
		"sign non-200":        func(p *policyServer) { p.signStatus = http.StatusForbidden },
		"chain non-200":       func(p *policyServer) { p.chainStatus = http.StatusInternalServerError },
		"chain without certs": func(p *policyServer) { p.chainPEM = []byte("no pem here") },
		"corrupt leaf":        func(p *policyServer) { p.chainPEM = pemBlock("CERTIFICATE", []byte("garbage")) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			p := newPolicyServer(t)
			p.set(setup)
			_, err := NewRemoteSigner(p.srv.URL).Sign(t.Context(), []byte("p"))
			assert.Error(t, err)
		})
	}

	for _, path := range []string{"/v1/sign", "/v1/chain"} {
		for _, mode := range []string{"hangup", "truncate"} {
			t.Run(path+" "+mode, func(t *testing.T) {
				p := newPolicyServer(t)
				p.set(func(p *policyServer) { p.broken = map[string]string{path: mode} })
				_, err := NewRemoteSigner(p.srv.URL).Sign(t.Context(), []byte("p"))
				assert.Error(t, err)
			})
		}
	}

	t.Run("unreachable", func(t *testing.T) {
		p := newPolicyServer(t)
		p.srv.Close()
		_, err := NewRemoteSigner(p.srv.URL).Sign(t.Context(), []byte("p"))
		assert.Error(t, err)
	})

	t.Run("invalid base URL", func(t *testing.T) {
		_, err := NewRemoteSigner("http://[::1").Sign(t.Context(), []byte("p"))
		assert.Error(t, err)
	})
}

func TestRemoteSignerRefreshesChainAfterRotation(t *testing.T) {
	p := newPolicyServer(t)
	rs := NewRemoteSigner(p.srv.URL)

	res, err := rs.Sign(t.Context(), []byte("p"))
	require.NoError(t, err)
	p.assertVerifies(t, res)

	p.rotate() // the cached chain is now stale
	res, err = rs.Sign(t.Context(), []byte("p"))
	require.NoError(t, err)
	p.assertVerifies(t, res)
	assert.EqualValues(t, 2, p.chainFetches.Load())

	_, err = rs.Sign(t.Context(), []byte("p"))
	require.NoError(t, err)
	assert.EqualValues(t, 2, p.chainFetches.Load(), "refreshed chain should be cached")
}

func TestRemoteSignerRefreshFailure(t *testing.T) {
	p := newPolicyServer(t)
	rs := NewRemoteSigner(p.srv.URL)
	_, err := rs.Sign(t.Context(), []byte("p"))
	require.NoError(t, err)

	p.rotate()
	p.set(func(p *policyServer) { p.chainStatus = http.StatusServiceUnavailable })
	_, err = rs.Sign(t.Context(), []byte("p"))
	assert.Error(t, err)
}

// A service whose chain never matches its signing key must not produce a result.
func TestRemoteSignerPersistentMismatch(t *testing.T) {
	p := newPolicyServer(t)
	other, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, p.root, p.rootKy)
	p.set(func(p *policyServer) { p.chainPEM = pemCert(other) })

	_, err := NewRemoteSigner(p.srv.URL).Sign(t.Context(), []byte("p"))
	assert.ErrorIs(t, err, ErrSignatureMismatch)
	assert.EqualValues(t, 2, p.chainFetches.Load())
}

func signConcurrently(t *testing.T, p *policyServer, rs *RemoteSigner, n int) {
	t.Helper()
	var wg sync.WaitGroup
	results := make([]SignResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = rs.Sign(context.Background(), []byte("p"))
		}(i)
	}
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		p.assertVerifies(t, results[i])
	}
}

func TestRemoteSignerCoalescesConcurrentFetches(t *testing.T) {
	const n = 50
	p := newPolicyServer(t)
	// Hold each chain response until every /v1/sign in the burst has been
	// served, so all callers overlap with the in-flight fetch.
	target := atomic.Int32{}
	p.set(func(p *policyServer) {
		p.chainGate = func() {
			deadline := time.Now().Add(5 * time.Second)
			for p.signs.Load() < target.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	rs := NewRemoteSigner(p.srv.URL)

	target.Store(n)
	signConcurrently(t, p, rs, n)
	assert.EqualValues(t, 1, p.chainFetches.Load(), "cold cache")

	p.rotate()
	target.Store(2 * n)
	signConcurrently(t, p, rs, n)
	assert.EqualValues(t, 2, p.chainFetches.Load(), "one shared refresh after rotation")
}

func TestRemoteSignerWaiterHonoursContext(t *testing.T) {
	p := newPolicyServer(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	p.set(func(p *policyServer) {
		p.chainGate = func() {
			entered <- struct{}{}
			<-release
		}
	})
	rs := NewRemoteSigner(p.srv.URL)

	leaderDone := make(chan error, 1)
	go func() {
		_, err := rs.Sign(context.Background(), []byte("p"))
		leaderDone <- err
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := rs.Sign(ctx, []byte("p"))
		waiterDone <- err
	}()
	for p.signs.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the waiter join the in-flight fetch
	cancel()
	assert.ErrorIs(t, <-waiterDone, context.Canceled)

	close(release)
	assert.NoError(t, <-leaderDone)
}

func TestRemoteSignerTrustAnchorPEM(t *testing.T) {
	p := newPolicyServer(t)

	got, err := NewRemoteSigner(p.srv.URL).TrustAnchorPEM(t.Context())
	require.NoError(t, err)
	assert.Equal(t, pemCert(p.root), got)

	p.set(func(p *policyServer) { p.caStatus = http.StatusNotFound })
	_, err = NewRemoteSigner(p.srv.URL).TrustAnchorPEM(t.Context())
	assert.Error(t, err)

	p.set(func(p *policyServer) { p.broken = map[string]string{"/v1/ca": "truncate"} })
	_, err = NewRemoteSigner(p.srv.URL).TrustAnchorPEM(t.Context())
	assert.Error(t, err)

	_, err = NewRemoteSigner("http://[::1").TrustAnchorPEM(t.Context())
	assert.Error(t, err)

	p.srv.Close()
	_, err = NewRemoteSigner(p.srv.URL).TrustAnchorPEM(t.Context())
	assert.Error(t, err)
}
