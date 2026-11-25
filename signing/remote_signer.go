package signing

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Limits on responses from the signing service, so a faulty or compromised
// service cannot exhaust memory. A signature response is capped at 64 KiB,
// far above the largest signature (512 bytes for RSA-4096). A chain or CA
// response is capped at 1 MiB; a PEM chain or root is a few kilobytes, so
// this leaves room for long chains and large keys.
const (
	maxSignResponseSize = 64 << 10
	maxCertResponseSize = 1 << 20
)

var errResponseTooLarge = errors.New("remote signer: response too large")

// readLimited reads all of r, failing rather than truncating when it holds
// more than limit bytes.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: more than %d bytes", errResponseTooLarge, limit)
	}
	return data, nil
}

// maxErrorBodySize bounds how much of an error response's body is quoted in
// the returned error, so a faulty or compromised service cannot flood logs.
const maxErrorBodySize = 256

// errorBody renders an error response's body for an error message: quoted,
// so newlines and terminal escape sequences cannot forge or corrupt log
// lines, and truncated to maxErrorBodySize bytes.
func errorBody(body []byte) string {
	if len(body) <= maxErrorBodySize {
		return fmt.Sprintf("%q", body)
	}
	return fmt.Sprintf("%q... (%d bytes)", body[:maxErrorBodySize], len(body))
}

// RemoteSigner implements [Signer] by delegating to an out-of-process HTTP
// signing service, so the OpAMP server holds no private key. It must expose:
//
//	POST /v1/sign  — body: payload bytes; response: raw signature bytes
//	GET  /v1/chain — response: PEM chain (intermediates first, leaf last, no root)
//	GET  /v1/ca    — response: PEM root CA (see TrustAnchorPEM)
//
// The service is a signing oracle: anything that reaches /v1/sign obtains
// signatures that every attested Agent accepts. It MUST authenticate its
// callers (mutual TLS is recommended, configured through
// [RemoteSignerSettings].Client), MUST NOT be reachable from Agents or the
// network edge, and SHOULD apply policy to each payload before signing it.
//
// For the same reason, RemoteSigner requires https and deliberately has no
// plaintext mode, not even for loopback, Unix socket, or service mesh
// deployments. Over plaintext, payloads, which may carry secrets such as
// private keys in connection settings, are exposed, and an on-path attacker
// can substitute the chain, signature, and root CA together, enrolling every
// TOFU Agent onto its own anchor. Where TLS to the service is not possible,
// implement [Signer] directly.
//
// The chain is cached, and concurrent fetches are coalesced into one
// request. Because signature and chain come from separate
// requests, Sign checks that the signature verifies under the chain's leaf
// and that the chain is within its validity period, refetching the chain
// once if not (after a leaf rotation, or a renewal under the same key), so
// it never returns a signature paired with a stale chain.
type RemoteSigner struct {
	baseURL string
	client  *http.Client
	timeout time.Duration // per request, however client is configured

	chainTTL    time.Duration
	mu          sync.Mutex
	cachedChain [][]byte
	cachedAt    time.Time
	// Concurrent callers share one in-flight fetch rather than each hitting
	// /v1/chain: fetching on a cache miss, refreshing after a rotation is
	// detected. Separate slots keep a refresh from joining a fetch that may
	// have started before the rotation.
	fetching   *chainFetch
	refreshing *chainFetch
}

// chainFetch is one in-flight /v1/chain request; done is closed once chain
// and err are set.
type chainFetch struct {
	done  chan struct{}
	chain [][]byte
	err   error
}

var (
	_ Signer              = (*RemoteSigner)(nil)
	_ TrustAnchorProvider = (*RemoteSigner)(nil)
)

const (
	defaultChainCacheTTL  = 60 * time.Second
	defaultRequestTimeout = 10 * time.Second
)

// ErrInvalidRemoteSignerURL is returned by [NewRemoteSigner] when the base URL
// does not parse, has no host, or is not https.
var ErrInvalidRemoteSignerURL = errors.New("remote signer: invalid signing service URL")

// RemoteSignerSettings configures a [RemoteSigner].
type RemoteSignerSettings struct {
	// BaseURL is the signing service's URL, such as
	// "https://policy-server:4322". It MUST use https; see [RemoteSigner].
	BaseURL string

	// Client sends every request. Configure it to authenticate to the
	// service, for example with a client certificate in its Transport's
	// TLSClientConfig, or a RoundTripper that adds credentials, and to trust
	// the service's certificate. If nil, a client with default TLS settings
	// and no client authentication is used. Redirects are never followed,
	// whatever Client's CheckRedirect, since a redirect would re-send the
	// payload wherever it points; Client itself is not modified.
	Client *http.Client

	// RequestTimeout bounds each request, even if Client has no Timeout. If
	// not positive, 10 seconds is used.
	RequestTimeout time.Duration

	// ChainCacheTTL is how long Sign reuses a fetched chain before fetching
	// it again to detect rotation. Zero means 60 seconds; a negative value
	// disables caching, so every Sign fetches the chain.
	ChainCacheTTL time.Duration
}

// NewRemoteSigner returns a RemoteSigner for the signing service described by
// settings. It returns ErrInvalidRemoteSignerURL unless BaseURL is an https
// URL with a host: there is no plaintext mode (see [RemoteSigner]).
func NewRemoteSigner(settings RemoteSignerSettings) (*RemoteSigner, error) {
	u, err := url.Parse(settings.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRemoteSignerURL, err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%w: %q does not use https", ErrInvalidRemoteSignerURL, settings.BaseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%w: %q has no host", ErrInvalidRemoteSignerURL, settings.BaseURL)
	}

	var client http.Client
	if settings.Client != nil {
		client = *settings.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	timeout := settings.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	ttl := settings.ChainCacheTTL
	if ttl == 0 {
		ttl = defaultChainCacheTTL
	}
	return &RemoteSigner{
		baseURL:  strings.TrimRight(settings.BaseURL, "/"),
		client:   &client,
		timeout:  timeout,
		chainTTL: ttl,
	}, nil
}

// Sign implements [Signer]. The service signs the posted bytes as-is, so
// SignResult.Payload is the input. If, even after refetching the chain, the
// signature does not verify under it or it is outside its validity period
// (ErrChainValidation), Sign returns an error. The service should rotate its
// signing certificate well before it expires.
func (s *RemoteSigner) Sign(ctx context.Context, payload []byte) (SignResult, error) {
	sig, err := s.sign(ctx, payload)
	if err != nil {
		return SignResult{}, err
	}

	chain, err := s.chainDER(ctx)
	if err != nil {
		return SignResult{}, err
	}
	if err := checkChain(chain, payload, sig); err != nil {
		// The chain is most likely stale: the leaf rotated after it was
		// cached, or was renewed under the same key and the cached copy
		// expired.
		chain, err = s.refreshChain(ctx)
		if err != nil {
			return SignResult{}, err
		}
		if err := checkChain(chain, payload, sig); err != nil {
			return SignResult{}, fmt.Errorf("remote signer: %w", err)
		}
	}
	return SignResult{Payload: payload, Signature: sig, ChainDER: chain}, nil
}

// sign POSTs payload to /v1/sign and returns the raw signature bytes.
func (s *RemoteSigner) sign(ctx context.Context, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/v1/sign", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("remote signer: build sign request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote signer: sign request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readLimited(resp.Body, maxSignResponseSize)
	if err != nil {
		return nil, fmt.Errorf("remote signer: read sign response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote signer: sign returned HTTP %d: %s", resp.StatusCode, errorBody(body))
	}
	return body, nil
}

// checkChain fails unless signature verifies under chain's leaf and chain is
// within its validity period now.
func checkChain(chain [][]byte, payload, signature []byte) error {
	if err := verifyUnderLeaf(chain, payload, signature); err != nil {
		return fmt.Errorf("signature does not verify under the current chain: %w", err)
	}
	return checkChainWindow(chain, time.Now())
}

// verifyUnderLeaf checks that signature verifies over payload under the
// public key of chain's leaf (the last entry).
func verifyUnderLeaf(chain [][]byte, payload, signature []byte) error {
	leaf, err := x509.ParseCertificate(chain[len(chain)-1])
	if err != nil {
		return fmt.Errorf("%w: leaf: %v", ErrParseCertificate, err)
	}
	return verifyWithCert(leaf, payload, signature)
}

// checkChainWindow fails if chain is outside its validity period at now.
func checkChainWindow(chain [][]byte, now time.Time) error {
	certs := make([]*x509.Certificate, len(chain))
	for i, der := range chain {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return fmt.Errorf("%w: chain[%d]: %v", ErrParseCertificate, i, err)
		}
		certs[i] = cert
	}
	notBefore, notAfter := chainWindow(certs)
	return checkSigningWindow(notBefore, notAfter, now)
}

// refreshChain fetches and caches the current chain, bypassing the cache.
func (s *RemoteSigner) refreshChain(ctx context.Context) ([][]byte, error) {
	s.mu.Lock()
	return s.sharedFetch(ctx, &s.refreshing)
}

// TrustAnchorPEM implements [TrustAnchorProvider] by GET-ing /v1/ca on the
// remote policy server. The response MUST be a PEM-encoded CA certificate.
func (s *RemoteSigner) TrustAnchorPEM(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.baseURL+"/v1/ca", nil)
	if err != nil {
		return nil, fmt.Errorf("remote signer: build CA request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote signer: CA request: %w", err)
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body, maxCertResponseSize)
	if err != nil {
		return nil, fmt.Errorf("remote signer: read CA response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote signer: CA returned HTTP %d: %s", resp.StatusCode, errorBody(body))
	}
	return body, nil
}

// chainDER returns the cached signing chain if it is younger than chainTTL,
// and otherwise fetches and caches the current one.
func (s *RemoteSigner) chainDER(ctx context.Context) ([][]byte, error) {
	s.mu.Lock()
	if s.cachedChain != nil && s.chainTTL > 0 && time.Since(s.cachedAt) < s.chainTTL {
		chain := s.cachedChain
		s.mu.Unlock()
		return chain, nil
	}
	return s.sharedFetch(ctx, &s.fetching)
}

// sharedFetch joins the fetch in *slot if one is running, or starts one and
// caches its result. The caller must hold s.mu; sharedFetch releases it.
func (s *RemoteSigner) sharedFetch(ctx context.Context, slot **chainFetch) ([][]byte, error) {
	f := *slot
	if f == nil {
		f = &chainFetch{done: make(chan struct{})}
		*slot = f
		// The fetch serves every waiter, so one caller's cancellation must not
		// fail it; the per-request timeout still bounds it.
		go s.runFetch(context.WithoutCancel(ctx), slot, f)
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		return f.chain, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runFetch performs the fetch for f, caches a successful result, and
// publishes it to f's waiters.
func (s *RemoteSigner) runFetch(ctx context.Context, slot **chainFetch, f *chainFetch) {
	chain, err := s.fetchChainDER(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.cachedChain = chain
		s.cachedAt = time.Now()
	}
	*slot = nil
	f.chain, f.err = chain, err
	close(f.done)
}

// fetchChainDER fetches /v1/chain. Its deadline matters most here: the shared
// fetch runs detached from any caller's context.
func (s *RemoteSigner) fetchChainDER(ctx context.Context) ([][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.baseURL+"/v1/chain", nil)
	if err != nil {
		return nil, fmt.Errorf("remote signer: build chain request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote signer: chain request: %w", err)
	}
	defer resp.Body.Close()

	pemBytes, err := readLimited(resp.Body, maxCertResponseSize)
	if err != nil {
		return nil, fmt.Errorf("remote signer: read chain response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote signer: chain returned HTTP %d: %s", resp.StatusCode, errorBody(pemBytes))
	}

	chain := pemCertificates(pemBytes)
	if len(chain) == 0 {
		return nil, fmt.Errorf("remote signer: chain response contained no CERTIFICATE PEM blocks")
	}
	for i, der := range chain {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("%w: chain[%d]: %v", ErrParseCertificate, i, err)
		}
		if isSelfSigned(cert) {
			return nil, fmt.Errorf("%w: chain[%d] is a self-signed root, which must not be included", ErrChainValidation, i)
		}
	}
	return chain, nil
}
