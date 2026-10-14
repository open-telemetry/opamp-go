// Package signing implements payload trust verification (Message
// Attestation) for the OpAMP protocol.
//
// A [Signer] produces a detached signature over the exact wire bytes of a
// marshalled ServerToAgent, carried in SignedServerToAgent.payload; a
// [Verifier] validates the delivered certificate chain and checks each
// signature over the bytes as received, so no re-marshalling is needed.
// [LocalSigner] and [LocalVerifier] are in-process reference
// implementations; other signers (HSM, remote signing services) plug in
// through the same interfaces.
//
// The signature algorithm is determined by the signing leaf's public key;
// the protocol does not negotiate algorithms. [GenerateCA] and
// [GenerateLeaf] are helpers for tests and examples.
package signing
