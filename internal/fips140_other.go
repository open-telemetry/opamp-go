//go:build !go1.26

package internal

// WithoutFIPSEnforcement runs f. Go releases before 1.26 have no
// crypto/fips140.WithoutEnforcement, so there is nothing to disable.
func WithoutFIPSEnforcement(f func()) {
	f()
}
