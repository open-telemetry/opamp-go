package server

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

const attestHost = "opamp.test"

type attestPKI struct {
	ca    *x509.Certificate
	caKey crypto.Signer
	caPEM []byte
}

func newAttestPKI(t *testing.T) *attestPKI {
	t.Helper()
	ca, key, err := signing.GenerateCA(signing.AlgorithmECDSAP256SHA256, signing.CertOptions{})
	require.NoError(t, err)
	return &attestPKI{ca: ca, caKey: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})}
}

func (p *attestPKI) signer(t *testing.T) *signing.LocalSigner {
	t.Helper()
	leaf, key, err := signing.GenerateLeaf(signing.AlgorithmECDSAP256SHA256, p.ca, p.caKey, signing.CertOptions{DNSNames: []string{attestHost}})
	require.NoError(t, err)
	s, err := signing.NewLocalSigner(key, []*x509.Certificate{leaf})
	require.NoError(t, err)
	return s
}

// verifyEnvelope validates env's chain (or pinned, if nil) and signature,
// returning the inner ServerToAgent.
func (p *attestPKI) verifyEnvelope(t *testing.T, env *protobufs.SignedServerToAgent) *protobufs.ServerToAgent {
	t.Helper()
	require.NotNil(t, env.TrustChainResponse)
	var chain [][]byte
	rest := env.TrustChainResponse.CertificateChain
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		chain = append(chain, b.Bytes)
	}
	v, err := signing.VerifierFromPEM(p.caPEM)
	require.NoError(t, err)
	vc, err := v.ValidateChain(context.Background(), chain, time.Now(), attestHost)
	require.NoError(t, err)
	require.NoError(t, v.Verify(context.Background(), env.Payload, env.Signature, vc))
	var msg protobufs.ServerToAgent
	require.NoError(t, proto.Unmarshal(env.Payload, &msg))
	return &msg
}

// rotatingSigner delegates to a swappable Signer and offers anchor for TOFU.
type rotatingSigner struct {
	mu     sync.Mutex
	cur    signing.Signer
	anchor []byte
}

func (r *rotatingSigner) TrustAnchorPEM(context.Context) ([]byte, error) { return r.anchor, nil }

func (r *rotatingSigner) set(s signing.Signer) {
	r.mu.Lock()
	r.cur = s
	r.mu.Unlock()
}

func (r *rotatingSigner) Sign(ctx context.Context, payload []byte) (signing.SignResult, error) {
	r.mu.Lock()
	s := r.cur
	r.mu.Unlock()
	return s.Sign(ctx, payload)
}

// signerFunc adapts a function to signing.Signer (no TrustAnchorProvider).
type signerFunc func(context.Context, []byte) (signing.SignResult, error)

func (f signerFunc) Sign(ctx context.Context, p []byte) (signing.SignResult, error) { return f(ctx, p) }

// anchorErrSigner is a Signer whose TrustAnchorPEM fails.
type anchorErrSigner struct{ signing.Signer }

func (anchorErrSigner) TrustAnchorPEM(context.Context) ([]byte, error) {
	return nil, errors.New("no anchor")
}
