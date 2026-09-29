package signing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateChainSuccess(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)

	t.Run("leaf only", func(t *testing.T) {
		leaf, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)
		vc, err := ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), testHost)
		require.NoError(t, err)
		assert.Equal(t, leaf.Raw, vc.Leaf().Raw)
		assert.NoError(t, vc.ValidAt(time.Now()))
	})

	t.Run("with intermediate", func(t *testing.T) {
		interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		inter := issueCert(t, &x509.Certificate{
			IsCA:                  true,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageCertSign,
		}, root, &interKey.PublicKey, rootKey)
		leaf, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, inter, interKey)

		vc, err := ValidateChain(t.Context(), ders(inter, leaf), rootPool(root), time.Now(), testHost)
		require.NoError(t, err)
		assert.Equal(t, leaf.Raw, vc.Leaf().Raw)

		// Without the intermediate the leaf cannot reach the root.
		_, err = ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), testHost)
		assert.ErrorIs(t, err, ErrChainValidation)
	})
}

func TestValidateChainInputErrors(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)

	_, err := ValidateChain(t.Context(), nil, rootPool(root), time.Now(), testHost)
	assert.ErrorIs(t, err, ErrEmptyChain)

	_, err = ValidateChain(t.Context(), ders(leaf), nil, time.Now(), testHost)
	assert.ErrorIs(t, err, ErrChainValidation)

	_, err = ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), "")
	assert.ErrorIs(t, err, ErrServerNameRequired)

	_, err = ValidateChain(t.Context(), [][]byte{[]byte("not a certificate")}, rootPool(root), time.Now(), testHost)
	assert.ErrorIs(t, err, ErrParseCertificate)
}

func TestValidateChainRejections(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)

	t.Run("unknown issuer", func(t *testing.T) {
		other, _ := newTestCA(t, AlgorithmECDSAP256SHA256)
		_, err := ValidateChain(t.Context(), ders(leaf), rootPool(other), time.Now(), testHost)
		assert.ErrorIs(t, err, ErrChainValidation)
	})

	t.Run("expired", func(t *testing.T) {
		_, err := ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now().Add(48*time.Hour), testHost)
		assert.ErrorIs(t, err, ErrChainValidation)
	})

	t.Run("missing code signing EKU", func(t *testing.T) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		tlsLeaf := issueCert(t, &x509.Certificate{
			KeyUsage:    x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames:    []string{testHost},
		}, root, &key.PublicKey, rootKey)
		_, err = ValidateChain(t.Context(), ders(tlsLeaf), rootPool(root), time.Now(), testHost)
		assert.ErrorIs(t, err, ErrChainValidation)
	})

	t.Run("hostname mismatch", func(t *testing.T) {
		_, err := ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), "attacker.example.com")
		assert.ErrorIs(t, err, ErrHostnameMismatch)
		assert.NotErrorIs(t, err, ErrChainValidation)
	})
}

func TestValidateChainMultipleSANs(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey, "gateway.example.com", "origin.example.com")

	for _, host := range []string{"gateway.example.com", "origin.example.com"} {
		_, err := ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), host)
		assert.NoError(t, err, host)
	}
	_, err := ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), "other.example.com")
	assert.ErrorIs(t, err, ErrHostnameMismatch)
}

func TestValidateChainIPSAN(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, _, err := GenerateLeaf(AlgorithmECDSAP256SHA256, root, rootKey, CertOptions{
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	})
	require.NoError(t, err)

	_, err = ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), "127.0.0.1")
	assert.NoError(t, err)
	_, err = ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), "127.0.0.2")
	assert.ErrorIs(t, err, ErrHostnameMismatch)
}

func TestVerifiedCertificateValidAtAfterExpiry(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, _, err := GenerateLeaf(AlgorithmECDSAP256SHA256, root, rootKey, CertOptions{
		DNSNames: []string{testHost},
		NotAfter: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	vc, err := ValidateChain(t.Context(), ders(leaf), rootPool(root), time.Now(), testHost)
	require.NoError(t, err)
	assert.NoError(t, vc.ValidAt(time.Now()))
	assert.ErrorIs(t, vc.ValidAt(time.Now().Add(2*time.Hour)), ErrChainValidation)
}
