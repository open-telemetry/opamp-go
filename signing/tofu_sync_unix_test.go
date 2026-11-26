//go:build unix

package signing

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSyncDir(t *testing.T) {
	assert.NoError(t, syncDir(t.TempDir()))
	assert.Error(t, syncDir(filepath.Join(t.TempDir(), "missing")))
}
