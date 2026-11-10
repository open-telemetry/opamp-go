// Package fips140test checks that the OpAMP WebSocket transport works when the
// binary runs with GODEBUG=fips140=only. The test file selects that mode with a
// //go:debug directive, which applies to the whole test binary, so the test
// lives in its own package. It only builds on Go 1.26 and later, where
// crypto/fips140.WithoutEnforcement exists; on older toolchains the handshake
// still panics in strict mode and the package has no tests.
package fips140test
