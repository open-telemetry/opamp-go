package signing

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RemoteSigner implements [Signer] by delegating to an out-of-process HTTP
// signing service, so the OpAMP server holds no private key. The service
// may also enforce policy on the payload before signing. It must expose:
//
//	POST /v1/sign  — body: payload bytes; response: raw signature bytes
//	GET  /v1/chain — response: PEM chain (intermediates first, leaf last, no root)
//	GET  /v1/ca    — response: PEM root CA (see TrustAnchorPEM)
//
// The chain is cached, and concurrent fetches are coalesced into one
// request. Because signature and chain come from separate
// requests, Sign checks the signature under the chain's leaf and refetches
// the chain once on a mismatch (a leaf rotation), so it never returns a
// signature paired with a stale chain.
type RemoteSigner struct {
	baseURL string
	client  *http.Client

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

const defaultChainCacheTTL = 60 * time.Second

// NewRemoteSigner returns a RemoteSigner that calls the signing service at
// baseURL (e.g. "http://policy-server:4322"). A 10-second per-request
// timeout is applied.
func NewRemoteSigner(baseURL string) *RemoteSigner {
	return &RemoteSigner{
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   &http.Client{Timeout: 10 * time.Second},
		chainTTL: defaultChainCacheTTL,
	}
}

// SetChainCacheTTL overrides how long Sign caches the fetched chain before
// re-fetching to detect rotation. A non-positive value disables caching
// (fetch on every Sign).
func (s *RemoteSigner) SetChainCacheTTL(ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chainTTL = ttl
}

// Sign implements [Signer]. The service signs the posted bytes as-is, so
// SignResult.Payload is the input. If the signature does not verify under
// the current chain even after a refetch, Sign returns an error.
func (s *RemoteSigner) Sign(ctx context.Context, payload []byte) (SignResult, error) {
	sig, err := s.sign(ctx, payload)
	if err != nil {
		return SignResult{}, err
	}

	chain, err := s.chainDER(ctx)
	if err != nil {
		return SignResult{}, err
	}
	if err := verifyUnderLeaf(chain, payload, sig); err != nil {
		// The leaf most likely rotated after the chain was cached.
		chain, err = s.refreshChain(ctx)
		if err != nil {
			return SignResult{}, err
		}
		if err := verifyUnderLeaf(chain, payload, sig); err != nil {
			return SignResult{}, fmt.Errorf("remote signer: signature does not verify under the current chain: %w", err)
		}
	}
	return SignResult{Payload: payload, Signature: sig, ChainDER: chain}, nil
}

// sign POSTs payload to /v1/sign and returns the raw signature bytes.
func (s *RemoteSigner) sign(ctx context.Context, payload []byte) ([]byte, error) {
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("remote signer: read sign response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote signer: sign returned HTTP %d: %s", resp.StatusCode, body)
	}
	return body, nil
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

// refreshChain fetches and caches the current chain, bypassing the cache.
func (s *RemoteSigner) refreshChain(ctx context.Context) ([][]byte, error) {
	s.mu.Lock()
	return s.sharedFetch(ctx, &s.refreshing)
}

// TrustAnchorPEM implements [TrustAnchorProvider] by GET-ing /v1/ca on the
// remote policy server. The response MUST be a PEM-encoded CA certificate.
func (s *RemoteSigner) TrustAnchorPEM(ctx context.Context) ([]byte, error) {
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("remote signer: read CA response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote signer: CA returned HTTP %d: %s", resp.StatusCode, body)
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
	if f := *slot; f != nil {
		s.mu.Unlock()
		select {
		case <-f.done:
			return f.chain, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &chainFetch{done: make(chan struct{})}
	*slot = f
	s.mu.Unlock()

	chain, err := s.fetchChainDER(ctx)

	s.mu.Lock()
	if err == nil {
		s.cachedChain = chain
		s.cachedAt = time.Now()
	}
	*slot = nil
	f.chain, f.err = chain, err
	close(f.done)
	s.mu.Unlock()
	return chain, err
}

func (s *RemoteSigner) fetchChainDER(ctx context.Context) ([][]byte, error) {
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

	pemBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("remote signer: read chain response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote signer: chain returned HTTP %d: %s", resp.StatusCode, pemBytes)
	}

	chain := pemCertificates(pemBytes)
	if len(chain) == 0 {
		return nil, fmt.Errorf("remote signer: chain response contained no CERTIFICATE PEM blocks")
	}
	return chain, nil
}
