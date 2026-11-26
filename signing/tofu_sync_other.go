//go:build !unix

package signing

// syncDir is a no-op on platforms without directory fsync (e.g. Windows).
func syncDir(string) error { return nil }
