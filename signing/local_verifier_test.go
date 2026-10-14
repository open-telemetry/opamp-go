package signing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLocalVerifierNilRoots(t *testing.T) {
	_, err := NewLocalVerifier(nil)
	assert.ErrorIs(t, err, ErrNilRoots)
}

func TestLocalVerifier(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, key := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)
	v, err := NewLocalVerifier(rootPool(root))
	require.NoError(t, err)

	payload := []byte("payload")
	sig, err := signWithKey(key, AlgorithmECDSAP256SHA256, payload)
	require.NoError(t, err)

	vc, err := v.ValidateChain(t.Context(), ders(leaf), time.Now(), testHost)
	require.NoError(t, err)
	require.NoError(t, v.Verify(t.Context(), payload, sig, vc))

	assert.ErrorIs(t, v.Verify(t.Context(), []byte("other"), sig, vc), ErrSignatureMismatch)
	assert.Error(t, v.Verify(t.Context(), payload, sig, nil))
	assert.Error(t, v.Verify(t.Context(), payload, nil, vc))

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = v.ValidateChain(cancelled, ders(leaf), time.Now(), testHost)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, v.Verify(cancelled, payload, sig, vc), context.Canceled)
}

// A chain validated while still valid must be rejected once it has expired.
func TestLocalVerifierRejectsChainExpiredSinceValidation(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour)
	root, rootKey, err := GenerateCA(AlgorithmECDSAP256SHA256, CertOptions{
		NotBefore: past.Add(-time.Hour),
		NotAfter:  time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	leaf, key, err := GenerateLeaf(AlgorithmECDSAP256SHA256, root, rootKey, CertOptions{
		DNSNames:  []string{testHost},
		NotBefore: past.Add(-time.Hour),
		NotAfter:  past.Add(time.Hour),
	})
	require.NoError(t, err)

	v, err := NewLocalVerifier(rootPool(root))
	require.NoError(t, err)
	vc, err := v.ValidateChain(t.Context(), ders(leaf), past, testHost)
	require.NoError(t, err)

	sig, err := signWithKey(key, AlgorithmECDSAP256SHA256, []byte("p"))
	require.NoError(t, err)
	assert.ErrorIs(t, v.Verify(t.Context(), []byte("p"), sig, vc), ErrChainValidation)
}
