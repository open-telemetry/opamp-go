package signing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// TOFUStore persists the trust anchor acquired by TOFU enrollment.
type TOFUStore interface {
	// Load returns the stored PEM anchor, or nil if none is stored.
	Load() ([]byte, error)

	// Save persists pemBytes. It MUST be a no-op if an anchor is already
	// stored, so a valid anchor is never overwritten.
	Save(pemBytes []byte) error
}

// ErrTOFUStoreSave wraps failures to persist the TOFU trust anchor.
var ErrTOFUStoreSave = errors.New("signing: save TOFU trust anchor")

// FileTOFUStore implements [TOFUStore] with a single PEM file, created with
// 0o600 permissions on first Save. It is safe for concurrent use.
type FileTOFUStore struct {
	path string
}

// NewFileTOFUStore returns a FileTOFUStore at path, which need not exist yet.
func NewFileTOFUStore(path string) *FileTOFUStore {
	return &FileTOFUStore{path: path}
}

// Load reads the anchor, returning nil, nil if the file does not exist.
func (s *FileTOFUStore) Load() ([]byte, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("signing: load TOFU trust anchor: %w", err)
	}
	return data, nil
}

// Save writes pemBytes only if the file does not exist. It writes a temp
// file in the same directory and hard-links it into place; os.Link fails if
// the target exists, keeping Save write-once, and a crash mid-write can never
// leave a truncated anchor that would block future Saves.
func (s *FileTOFUStore) Save(pemBytes []byte) error {
	if _, err := os.Stat(s.path); err == nil {
		return nil // idempotent: already stored
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tofu-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}
	tmpName := tmp.Name()
	// The temp name is redundant after a successful link, so always remove it.
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}
	if _, err := tmp.Write(pemBytes); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}

	if err := os.Link(tmpName, s.path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil // idempotent: another writer won the race
		}
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}
	return nil
}
