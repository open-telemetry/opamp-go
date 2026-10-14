//go:build unix

package signing

import (
	"errors"
	"os"
	"syscall"
)

// syncDir fsyncs dir so a newly created entry survives a crash. Filesystems
// that do not support syncing a directory are tolerated.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}
