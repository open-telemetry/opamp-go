//go:build go1.26

package internal

import "crypto/fips140"

// WithoutFIPSEnforcement runs f with strict FIPS 140-3 enforcement disabled.
// It is used around the WebSocket handshake, which RFC 6455 defines with a
// SHA-1 digest that GODEBUG=fips140=only would otherwise refuse. The digest
// only confirms that both ends speak WebSocket, it is not a security function.
func WithoutFIPSEnforcement(f func()) {
	fips140.WithoutEnforcement(f)
}
