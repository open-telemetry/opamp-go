package internal

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

const testHost = "opamp.test"

type testPKI struct {
	ca    *x509.Certificate
	caKey crypto.Signer
	caPEM []byte
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	ca, key, err := signing.GenerateCA(signing.AlgorithmECDSAP256SHA256, signing.CertOptions{})
	require.NoError(t, err)
	return &testPKI{ca: ca, caKey: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})}
}

func (p *testPKI) signer(t *testing.T, hosts ...string) *signing.LocalSigner {
	t.Helper()
	if len(hosts) == 0 {
		hosts = []string{testHost}
	}
	leaf, key, err := signing.GenerateLeaf(signing.AlgorithmECDSAP256SHA256, p.ca, p.caKey, signing.CertOptions{DNSNames: hosts})
	require.NoError(t, err)
	s, err := signing.NewLocalSigner(key, []*x509.Certificate{leaf})
	require.NoError(t, err)
	return s
}

func (p *testPKI) verifier(t *testing.T) signing.Verifier {
	t.Helper()
	v, err := signing.VerifierFromPEM(p.caPEM)
	require.NoError(t, err)
	return v
}

func chainPEM(chainDER [][]byte) []byte {
	var out []byte
	for _, der := range chainDER {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	return out
}

// signedEnvelope signs payload with s; withChain attaches trust_chain_response.
func signedEnvelope(t *testing.T, s signing.Signer, payload []byte, withChain bool) *protobufs.SignedServerToAgent {
	t.Helper()
	res, err := s.Sign(context.Background(), payload)
	require.NoError(t, err)
	env := &protobufs.SignedServerToAgent{Payload: res.Payload, Signature: res.Signature}
	if withChain {
		env.TrustChainResponse = &protobufs.TrustChainResponse{CertificateChain: chainPEM(res.ChainDER)}
	}
	return env
}

func signedMsg(t *testing.T, s signing.Signer, msg *protobufs.ServerToAgent, withChain bool) *protobufs.SignedServerToAgent {
	t.Helper()
	return signedEnvelope(t, s, mustMarshal(t, msg), withChain)
}

// countingVerifier counts ValidateChain calls on an inner Verifier.
type countingVerifier struct {
	signing.Verifier
	validations int
}

func (c *countingVerifier) ValidateChain(ctx context.Context, chainDER [][]byte, now time.Time, dnsName string) (*signing.VerifiedCertificate, error) {
	c.validations++
	return c.Verifier.ValidateChain(ctx, chainDER, now, dnsName)
}

// errVerifier fails ValidateChain with err.
type errVerifier struct{ err error }

func (e errVerifier) ValidateChain(context.Context, [][]byte, time.Time, string) (*signing.VerifiedCertificate, error) {
	return nil, e.err
}

func (e errVerifier) Verify(context.Context, []byte, []byte, *signing.VerifiedCertificate) error {
	return e.err
}

// enrollerFunc adapts a function to signing.TOFUEnroller.
type enrollerFunc func([]byte) (signing.Verifier, error)

func (f enrollerFunc) Enroll(anchorPEM []byte) (signing.Verifier, error) { return f(anchorPEM) }

var testMsg = &protobufs.ServerToAgent{InstanceUid: []byte("0123456789abcdef"), Flags: 1}
