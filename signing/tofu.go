package signing

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// TOFUStore persists the trust anchor acquired by TOFU enrollment.
type TOFUStore interface {
	// Load returns the stored PEM anchor, or (nil, nil) if none is stored.
	// Stored data that cannot serve as an anchor, such as an empty file,
	// MUST be an error rather than "none stored": Save would then be a
	// no-op, and enrollment could never complete.
	Load() ([]byte, error)

	// Save persists pemBytes. It MUST be a no-op if an anchor is already
	// stored, so a valid anchor is never overwritten.
	Save(pemBytes []byte) error
}

// ErrTOFUStoreSave wraps failures to persist the TOFU trust anchor.
var ErrTOFUStoreSave = errors.New("signing: save TOFU trust anchor")

// ErrTOFUAnchorFileEmpty is returned by [FileTOFUStore.Load] when the anchor
// file exists but has no content. Save never replaces an existing file, so
// it must be deleted before the Agent can enroll.
var ErrTOFUAnchorFileEmpty = errors.New("signing: TOFU trust anchor file exists but is empty")

// FileTOFUStore implements [TOFUStore] with a single PEM file, created with
// 0o600 permissions on first Save. It is safe for concurrent use.
type FileTOFUStore struct {
	path string
}

// NewFileTOFUStore returns a FileTOFUStore at path, which need not exist yet.
func NewFileTOFUStore(path string) *FileTOFUStore {
	return &FileTOFUStore{path: path}
}

// Load reads the anchor, returning nil, nil if the file does not exist and
// [ErrTOFUAnchorFileEmpty] if it exists without content.
func (s *FileTOFUStore) Load() ([]byte, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("signing: load TOFU trust anchor: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: %s; delete it to re-enroll", ErrTOFUAnchorFileEmpty, s.path)
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
	// Persist the new directory entry, or a crash could lose the anchor and
	// let the next start enroll a different one.
	if err := syncDir(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("%w: %v", ErrTOFUStoreSave, err)
	}
	return nil
}

// ErrInvalidTOFUAnchor is returned when a TOFU trust anchor is not a single
// self-signed CA certificate, without id-kp-serverAuth, in PEM form.
var ErrInvalidTOFUAnchor = errors.New("signing: invalid TOFU trust anchor")

// TOFUAnchorFromPEM parses a TOFU trust anchor: exactly one PEM block, a
// CERTIFICATE holding a self-signed CA that does not carry id-kp-serverAuth
// (see [ErrTrustAnchorServerAuth]); text around the block is ignored. It
// returns a Verifier that trusts only that certificate, and the certificate
// re-encoded as PEM, which is what should be persisted.
//
// The spec defines the anchor as one root CA certificate. A bundle would make
// every certificate in it a permanent trust anchor, and a leaf or
// intermediate would stop verifying once it rotated.
func TOFUAnchorFromPEM(pemBytes []byte) (*LocalVerifier, []byte, error) {
	block, rest := pem.Decode(pemBytes)
	if block == nil {
		return nil, nil, fmt.Errorf("%w: no PEM block", ErrInvalidTOFUAnchor)
	}
	if extra, _ := pem.Decode(rest); extra != nil {
		return nil, nil, fmt.Errorf("%w: more than one PEM block", ErrInvalidTOFUAnchor)
	}
	if block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("%w: PEM block is %q, not CERTIFICATE", ErrInvalidTOFUAnchor, block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidTOFUAnchor, err)
	}
	if !cert.BasicConstraintsValid || !cert.IsCA {
		return nil, nil, fmt.Errorf("%w: not a CA certificate", ErrInvalidTOFUAnchor)
	}
	if !isSelfSigned(cert) {
		return nil, nil, fmt.Errorf("%w: not self-signed, so not a root", ErrInvalidTOFUAnchor)
	}
	if hasServerAuth(cert) {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidTOFUAnchor, ErrTrustAnchorServerAuth)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	v, err := NewLocalVerifier(pool)
	if err != nil {
		return nil, nil, err
	}
	return v, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}
