package signing

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RemoteSigner implements [Signer] by delegating to an out-of-process HTTP
// signing service. This is the recommended production deployment pattern
// for Message Attestation: the OpAMP distribution server holds no private
// key material, and signing is performed by a separate policy server that
// is not reachable from the network edge.
//
// The signing service must expose two endpoints:
//
//	POST /v1/sign  — request body: raw payload bytes to sign
//	               — response body: raw signature bytes
//	GET  /v1/chain — response body: PEM-encoded certificate chain
//	               — (intermediates first, signing leaf last, root excluded)
//
// In production the policy server may additionally enforce organizational
// policy — inspecting the decoded payload to deny message types, enforce
// per-team permissions, or apply fleet-wide invariants — before delegating
// to an HSM or secrets manager for the actual signature.
type RemoteSigner struct {
	baseURL string
	client  *http.Client

	// Sign fetches the chain from /v1/chain on every outbound message (so the
	// chain it returns tracks signing-chain rotation); the chain is cached for
	// chainTTL so that only one /v1/chain fetch happens per interval. A shorter
	// TTL detects rotation faster at the cost of more fetches.
	chainTTL    time.Duration
	mu          sync.Mutex
	cachedChain [][]byte
	cachedAt    time.Time
}

var _ Signer = (*RemoteSigner)(nil)
var _ TrustAnchorProvider = (*RemoteSigner)(nil)

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

// Sign implements [Signer] by POST-ing payload to /v1/sign for the detached
// signature and pairing it with the current chain from chainDER. This stand-in
// signs the exact bytes posted (it does not re-marshal), so SignResult.Payload
// is the input payload unchanged. A production signing backend that serializes
// server-side would instead return the bytes it actually signed here.
func (s *RemoteSigner) Sign(ctx context.Context, payload []byte) (SignResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/v1/sign", bytes.NewReader(payload))
	if err != nil {
		return SignResult{}, fmt.Errorf("remote signer: build sign request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := s.client.Do(req)
	if err != nil {
		return SignResult{}, fmt.Errorf("remote signer: sign request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SignResult{}, fmt.Errorf("remote signer: read sign response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return SignResult{}, fmt.Errorf("remote signer: sign returned HTTP %d: %s", resp.StatusCode, body)
	}

	chain, err := s.chainDER(ctx)
	if err != nil {
		return SignResult{}, err
	}
	return SignResult{Payload: payload, Signature: body, ChainDER: chain}, nil
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

// chainDER returns the current signing chain by GET-ing /v1/chain and decoding
// the returned PEM blob into DER byte slices ordered intermediates-first,
// leaf-last. It is called by Sign on every outbound message; results are cached
// for chainTTL to bound the fetch rate.
func (s *RemoteSigner) chainDER(ctx context.Context) ([][]byte, error) {
	s.mu.Lock()
	if s.cachedChain != nil && s.chainTTL > 0 && time.Since(s.cachedAt) < s.chainTTL {
		chain := s.cachedChain
		s.mu.Unlock()
		return chain, nil
	}
	s.mu.Unlock()

	chain, err := s.fetchChainDER(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cachedChain = chain
	s.cachedAt = time.Now()
	s.mu.Unlock()
	return chain, nil
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

	var chain [][]byte
	rest := pemBytes
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			chain = append(chain, block.Bytes)
		}
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("remote signer: chain response contained no CERTIFICATE PEM blocks")
	}
	return chain, nil
}
