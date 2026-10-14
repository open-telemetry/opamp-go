package signing

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlgorithmString(t *testing.T) {
	cases := map[Algorithm]string{
		AlgorithmUnspecified:       "unspecified",
		AlgorithmECDSAP256SHA256:   "ECDSA-P256-SHA256",
		AlgorithmECDSAP384SHA384:   "ECDSA-P384-SHA384",
		AlgorithmRSAPKCS1v15SHA256: "RSA-PKCS1v15-SHA256",
		AlgorithmEd25519:           "Ed25519",
		Algorithm(99):              "unspecified",
	}
	for alg, want := range cases {
		assert.Equal(t, want, alg.String())
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	payload := []byte("server-to-agent bytes")
	for _, alg := range allAlgorithms {
		t.Run(alg.String(), func(t *testing.T) {
			ca, caKey := newTestCA(t, alg)
			leaf, key := newTestLeaf(t, alg, ca, caKey)

			got, err := algorithmFromCert(leaf)
			require.NoError(t, err)
			assert.Equal(t, alg, got)

			sig, err := signWithKey(key, alg, payload)
			require.NoError(t, err)
			require.NoError(t, verifyWithCert(leaf, payload, sig))

			// A key whose concrete type is hidden (HSM, KMS) must sign too.
			sig, err = signWithKey(opaqueSigner{key}, alg, payload)
			require.NoError(t, err)
			require.NoError(t, verifyWithCert(leaf, payload, sig))

			err = verifyWithCert(leaf, []byte("tampered"), sig)
			assert.ErrorIs(t, err, ErrSignatureMismatch)
		})
	}
}

func TestAlgorithmFromCertUnsupported(t *testing.T) {
	p224, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	require.NoError(t, err)
	rsa1024, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	x25519, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)

	cases := map[string]any{
		"p224 curve":    &p224.PublicKey,
		"nil curve":     &ecdsa.PublicKey{},
		"rsa 1024":      &rsa1024.PublicKey,
		"rsa nil N":     &rsa.PublicKey{},
		"x25519 key":    x25519.PublicKey(),
		"no public key": nil,
	}
	for name, pub := range cases {
		t.Run(name, func(t *testing.T) {
			cert := &x509.Certificate{PublicKey: pub}
			alg, err := algorithmFromCert(cert)
			assert.ErrorIs(t, err, ErrUnsupportedAlgorithm)
			assert.Equal(t, AlgorithmUnspecified, alg)

			assert.ErrorIs(t, verifyWithCert(cert, []byte("p"), []byte("s")), ErrUnsupportedAlgorithm)
		})
	}
}

func TestSignWithKeyUnknownAlgorithm(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	_, err = signWithKey(key, AlgorithmUnspecified, []byte("p"))
	assert.ErrorIs(t, err, ErrUnsupportedAlgorithm)
}
