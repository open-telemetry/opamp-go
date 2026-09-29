package signing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileTOFUStoreLoadMissing(t *testing.T) {
	got, err := NewFileTOFUStore(filepath.Join(t.TempDir(), "anchor.pem")).Load()
	require.NoError(t, err)
	assert.Nil(t, got)
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
