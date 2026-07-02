package signing

import (
	"errors"
	"fmt"
	"os"
)

// TOFUStore persists the payload trust anchor acquired during a Trust On First
// Use (TOFU) enrollment. The Agent calls Save once — on first connection — and
// Load on every subsequent startup.
//
// Implementations MUST be idempotent on Save: if a trust anchor is already
// stored, a second Save call MUST be a no-op. This prevents a reconnecting
// agent from overwriting a valid anchor with a potentially attacker-supplied
// one.
type TOFUStore interface {
	// Load returns the PEM-encoded trust anchor bytes saved by a previous
	// Save call, or nil if no anchor has been stored yet.
	Load() ([]byte, error)

	// Save persists pemBytes as the trust anchor. Called at most once per
	// store lifetime; subsequent calls MUST be ignored if an anchor is
	// already present.
	Save(pemBytes []byte) error
}

// ErrTOFUStoreSave wraps failures to persist the TOFU trust anchor.
var ErrTOFUStoreSave = errors.New("signing: save TOFU trust anchor")

// FileTOFUStore implements [TOFUStore] by reading and writing a single PEM
// file. The file is created on first Save with 0o600 permissions. If the
// file already exists when Save is called, Save is a no-op (idempotent as
// required by the interface contract).
//
// The store is safe for concurrent use within one process, but does not use
// file locking; two processes writing to the same path concurrently may
// corrupt the file.
type FileTOFUStore struct {
	path string
}

// NewFileTOFUStore returns a FileTOFUStore that persists the trust anchor at
// path. The path does not need to exist yet; it is created on first Save.
func NewFileTOFUStore(path string) *FileTOFUStore {
	return &FileTOFUStore{path: path}
}

// Load reads the trust anchor from the file. Returns nil, nil if the file
// does not exist.
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

// Save writes pemBytes to the file only if the file does not already exist.
func (s *FileTOFUStore) Save(pemBytes []byte) error {
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil // idempotent: already stored
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}
	defer f.Close()
	if _, err := f.Write(pemBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}
	return nil
}
