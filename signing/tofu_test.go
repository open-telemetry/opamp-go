package signing

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileTOFUStoreLoadMissing(t *testing.T) {
	got, err := NewFileTOFUStore(filepath.Join(t.TempDir(), "anchor.pem")).Load()
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestFileTOFUStoreLoadEmptyFile(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "whitespace only": " \n\t\n"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "anchor.pem")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			got, err := NewFileTOFUStore(path).Load()
			require.ErrorIs(t, err, ErrTOFUAnchorFileEmpty)
			assert.ErrorContains(t, err, path)
			assert.Nil(t, got)
		})
	}
}

func TestFileTOFUStoreSaveIsWriteOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anchor.pem")
	s := NewFileTOFUStore(path)

	require.NoError(t, s.Save([]byte("first")))
	require.NoError(t, s.Save([]byte("second")))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), got)

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}

	// No temp files are left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestFileTOFUStoreErrors(t *testing.T) {
	dir := t.TempDir()

	err := NewFileTOFUStore(filepath.Join(dir, "missing-dir", "anchor.pem")).Save([]byte("x"))
	assert.ErrorIs(t, err, ErrTOFUStoreSave)

	// A path beneath a regular file fails Stat with something other than ErrNotExist.
	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	err = NewFileTOFUStore(filepath.Join(file, "anchor.pem")).Save([]byte("x"))
	assert.ErrorIs(t, err, ErrTOFUStoreSave)

	_, err = NewFileTOFUStore(dir).Load()
	require.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrNotExist))
}

func TestFileTOFUStoreConcurrentSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anchor.pem")
	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = NewFileTOFUStore(path).Save([]byte(fmt.Sprintf("anchor-%02d", i)))
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	got, err := NewFileTOFUStore(path).Load()
	require.NoError(t, err)
	assert.Regexp(t, `^anchor-\d\d$`, string(got))

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestTOFUAnchorFromPEM(t *testing.T) {
	alg := AlgorithmECDSAP256SHA256
	root, rootKey := newTestCA(t, alg)
	other, _ := newTestCA(t, alg)
	leaf, _ := newTestLeaf(t, alg, root, rootKey)
	interKey, err := newKey(alg)
	require.NoError(t, err)
	inter := issueCert(t, &x509.Certificate{IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, root, interKey.Public(), rootKey)
	nonCAKey, err := newKey(alg)
	require.NoError(t, err)
	nonCA := issueCert(t, &x509.Certificate{BasicConstraintsValid: true}, nil, nonCAKey.Public(), nonCAKey)

	t.Run("root CA", func(t *testing.T) {
		offered := append([]byte("Root CA for "+testHost+"\n"), pemCert(root)...)
		offered = append(offered, "\nsee the runbook\n"...)
		v, anchor, err := TOFUAnchorFromPEM(offered)
		require.NoError(t, err)
		assert.Equal(t, pemCert(root), anchor, "only the certificate is kept")
		_, err = v.ValidateChain(t.Context(), ders(leaf), time.Now(), testHost)
		require.NoError(t, err)
	})

	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})
	rejected := []struct {
		name, reason string
		input        []byte
	}{
		{"empty", "no PEM block", nil},
		{"no PEM block", "no PEM block", []byte("not a certificate")},
		{"bundle of two CAs", "more than one PEM block", append(pemCert(root), pemCert(other)...)},
		{"extra non-certificate block", "more than one PEM block", append(pemCert(root), keyBlock...)},
		{"non-certificate block", "not CERTIFICATE", keyBlock},
		{"malformed certificate", "x509", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")})},
		{"leaf", "not a CA", pemCert(leaf)},
		{"self-signed non-CA", "not a CA", pemCert(nonCA)},
		{"intermediate CA", "not self-signed", pemCert(inter)},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			v, anchor, err := TOFUAnchorFromPEM(tc.input)
			require.ErrorIs(t, err, ErrInvalidTOFUAnchor)
			assert.ErrorContains(t, err, tc.reason)
			assert.Nil(t, v)
			assert.Nil(t, anchor)
		})
	}
}
