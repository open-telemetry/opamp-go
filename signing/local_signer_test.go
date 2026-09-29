package signing

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noEqualSigner's public key has no Equal method.
type noEqualSigner struct{ crypto.Signer }

func (noEqualSigner) Public() crypto.PublicKey { return struct{}{} }
func (s noEqualSigner) Sign(r io.Reader, d []byte, o crypto.SignerOpts) ([]byte, error) {
	return s.Signer.Sign(r, d, o)
}

func TestNewLocalSignerErrors(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, key := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)

	_, err := NewLocalSigner(nil, []*x509.Certificate{leaf})
	assert.ErrorIs(t, err, ErrNilKey)

	_, err = NewLocalSigner(key, nil)
	assert.ErrorIs(t, err, ErrEmptyChain)

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	_, err = NewLocalSigner(other, []*x509.Certificate{leaf})
	assert.ErrorIs(t, err, ErrKeyMismatch)

	_, err = NewLocalSigner(noEqualSigner{key}, []*x509.Certificate{leaf})
	assert.ErrorIs(t, err, ErrKeyMismatch)

	p224, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	require.NoError(t, err)
	p224Leaf := issueCert(t, &x509.Certificate{
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}, root, &p224.PublicKey, rootKey)
	_, err = NewLocalSigner(p224, []*x509.Certificate{p224Leaf})
	assert.ErrorIs(t, err, ErrUnsupportedAlgorithm)
}

func TestNewLocalSignerInconsistentChain(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	cases := map[string]*x509.Certificate{
		"expired leaf": issueCert(t, &x509.Certificate{
			KeyUsage:    x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
			NotBefore:   time.Now().Add(-2 * time.Hour),
			NotAfter:    time.Now().Add(-time.Hour),
		}, root, &key.PublicKey, rootKey),
		"leaf without code signing EKU": issueCert(t, &x509.Certificate{
			KeyUsage:    x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}, root, &key.PublicKey, rootKey),
	}
	for name, leaf := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewLocalSigner(key, []*x509.Certificate{root, leaf})
			assert.ErrorIs(t, err, ErrChainValidation)
		})
	}

	t.Run("wrong order", func(t *testing.T) {
		leaf := issueCert(t, &x509.Certificate{
			KeyUsage:    x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		}, root, &key.PublicKey, rootKey)
		unrelated, _ := newTestCA(t, AlgorithmECDSAP256SHA256)
		_, err := NewLocalSigner(key, []*x509.Certificate{unrelated, leaf})
		assert.ErrorIs(t, err, ErrChainValidation)
	})
}

func TestLocalSignerSign(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP384SHA384)
	interKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	inter := issueCert(t, &x509.Certificate{
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, root, &interKey.PublicKey, rootKey)
	leaf, key := newTestLeaf(t, AlgorithmECDSAP384SHA384, inter, interKey)

	s, err := NewLocalSigner(key, []*x509.Certificate{inter, leaf})
	require.NoError(t, err)
	assert.Equal(t, AlgorithmECDSAP384SHA384, s.Algorithm())

	payload := []byte("payload")
	res, err := s.Sign(t.Context(), payload)
	require.NoError(t, err)
	assert.Equal(t, payload, res.Payload)
	assert.Equal(t, ders(inter, leaf), res.ChainDER)

	v, err := NewLocalVerifier(rootPool(root))
	require.NoError(t, err)
	vc, err := v.ValidateChain(t.Context(), res.ChainDER, time.Now(), testHost)
	require.NoError(t, err)
	require.NoError(t, v.Verify(t.Context(), res.Payload, res.Signature, vc))

	// Reassigning entries of a returned chain must not affect the signer.
	res.ChainDER[0] = nil
	res2, err := s.Sign(t.Context(), payload)
	require.NoError(t, err)
	assert.Equal(t, ders(inter, leaf), res2.ChainDER)
}

// failingSigner has the leaf's public key but cannot sign.
type failingSigner struct{ crypto.Signer }

func (failingSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("hsm unavailable")
}

func TestLocalSignerSignError(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, key := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)
	s, err := NewLocalSigner(failingSigner{key}, []*x509.Certificate{leaf})
	require.NoError(t, err)
	_, err = s.Sign(t.Context(), []byte("p"))
	assert.EqualError(t, err, "hsm unavailable")
}

// A chain that includes its root and an intermediate is internally consistent.
func TestNewLocalSignerFullChain(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	inter := issueCert(t, &x509.Certificate{
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, root, &interKey.PublicKey, rootKey)
	leaf, key := newTestLeaf(t, AlgorithmECDSAP256SHA256, inter, interKey)

	_, err = NewLocalSigner(key, []*x509.Certificate{root, inter, leaf})
	require.NoError(t, err)
}

func TestLocalSignerSignCancelledContext(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmEd25519)
	leaf, key := newTestLeaf(t, AlgorithmEd25519, root, rootKey)
	s, err := NewLocalSigner(key, []*x509.Certificate{leaf})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.Sign(ctx, []byte("p"))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestLocalSignerTrustAnchorPEM(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, key := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)
	s, err := NewLocalSigner(key, []*x509.Certificate{leaf})
	require.NoError(t, err)

	_, err = s.TrustAnchorPEM(t.Context())
	require.Error(t, err)

	assert.Same(t, s, s.WithRootCA(root))
	got, err := s.TrustAnchorPEM(t.Context())
	require.NoError(t, err)
	block, _ := pem.Decode(got)
	require.NotNil(t, block)
	assert.Equal(t, root.Raw, block.Bytes)

	// The returned bytes are a copy.
	got[0] = 'X'
	again, err := s.TrustAnchorPEM(t.Context())
	require.NoError(t, err)
	assert.Equal(t, pemCert(root), again)
}
