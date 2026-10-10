package signing

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTOFUStore struct {
	stored  []byte
	loadErr error
	saveErr error
	saves   int
}

func (f *fakeTOFUStore) Load() ([]byte, error) { return f.stored, f.loadErr }
func (f *fakeTOFUStore) Save(b []byte) error {
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	if f.stored == nil {
		f.stored = b
	}
	return nil
}

func TestFixedAnchor(t *testing.T) {
	root, _ := newTestCA(t, AlgorithmECDSAP256SHA256)
	v, err := NewLocalVerifier(rootPool(root))
	require.NoError(t, err)

	p := FixedAnchor(v)
	got, err := p.Verifier()
	require.NoError(t, err)
	assert.Same(t, v, got)
	_, isEnroller := p.(TOFUEnroller)
	assert.False(t, isEnroller)
}

func TestTOFUAnchorVerifier(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)

	v, err := TOFUAnchor(&fakeTOFUStore{}).Verifier()
	require.NoError(t, err)
	assert.Nil(t, v)

	boom := errors.New("boom")
	_, err = TOFUAnchor(&fakeTOFUStore{loadErr: boom}).Verifier()
	assert.ErrorIs(t, err, boom)

	v, err = TOFUAnchor(&fakeTOFUStore{stored: []byte("garbage")}).Verifier()
	assert.ErrorIs(t, err, ErrLoadCAFile)
	assert.Nil(t, v)

	v, err = TOFUAnchor(&fakeTOFUStore{stored: pemCert(root)}).Verifier()
	require.NoError(t, err)
	_, err = v.ValidateChain(t.Context(), ders(leaf), time.Now(), testHost)
	assert.NoError(t, err)
}

func TestTOFUAnchorEnroll(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)

	store := NewFileTOFUStore(filepath.Join(t.TempDir(), "anchor.pem"))
	p := TOFUAnchor(store)
	enroller, ok := p.(TOFUEnroller)
	require.True(t, ok)

	v, err := enroller.Enroll(pemCert(root))
	require.NoError(t, err)
	_, err = v.ValidateChain(t.Context(), ders(leaf), time.Now(), testHost)
	require.NoError(t, err)

	// A later startup loads the enrolled anchor.
	v, err = p.Verifier()
	require.NoError(t, err)
	require.NotNil(t, v)
}

// Once an anchor is stored, enrolling a different one must not grant it trust.
func TestTOFUAnchorEnrollReturnsStoredAnchor(t *testing.T) {
	anchorA, keyA := newTestCA(t, AlgorithmECDSAP256SHA256)
	anchorB, keyB := newTestCA(t, AlgorithmECDSAP256SHA256)
	leafA, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, anchorA, keyA)
	leafB, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, anchorB, keyB)

	store := &fakeTOFUStore{stored: pemCert(anchorA)}
	v, err := TOFUAnchor(store).(TOFUEnroller).Enroll(pemCert(anchorB))
	require.NoError(t, err)
	assert.Equal(t, pemCert(anchorA), store.stored)

	_, err = v.ValidateChain(t.Context(), ders(leafA), time.Now(), testHost)
	assert.NoError(t, err)
	_, err = v.ValidateChain(t.Context(), ders(leafB), time.Now(), testHost)
	assert.ErrorIs(t, err, ErrChainValidation)
}

func TestTOFUAnchorEnrollErrors(t *testing.T) {
	root, _ := newTestCA(t, AlgorithmECDSAP256SHA256)
	boom := errors.New("boom")

	store := &fakeTOFUStore{}
	_, err := TOFUAnchor(store).(TOFUEnroller).Enroll([]byte("garbage"))
	assert.ErrorIs(t, err, ErrLoadCAFile)
	assert.Zero(t, store.saves, "an unusable anchor must not be saved")

	_, err = TOFUAnchor(&fakeTOFUStore{saveErr: boom}).(TOFUEnroller).Enroll(pemCert(root))
	assert.ErrorIs(t, err, boom)

	_, err = TOFUAnchor(&fakeTOFUStore{loadErr: boom}).(TOFUEnroller).Enroll(pemCert(root))
	assert.ErrorIs(t, err, boom)

	// The stored anchor, not the offered one, must be usable.
	_, err = TOFUAnchor(&fakeTOFUStore{stored: []byte("corrupt")}).(TOFUEnroller).Enroll(pemCert(root))
	assert.ErrorIs(t, err, ErrLoadCAFile)
}
