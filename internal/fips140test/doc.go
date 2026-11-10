// Package fips140test checks that the OpAMP WebSocket transport works when the
// binary runs with GODEBUG=fips140=only. The test file selects that mode with a
// //go:debug directive, which applies to the whole test binary, so the test
// lives in its own package.
package fips140test
