// Package signing implements payload trust verification (Message
// Attestation) for the OpAMP protocol.
//
// The package exposes two interfaces — [Signer] and [Verifier] — together
// with in-process reference implementations [LocalSigner] and
// [LocalVerifier]. The split between interface and implementation lets
// downstream consumers plug in alternative signers backed by remote
// signing services (for example HSM-backed RPC endpoints or hosted
// signing platforms with policy gating) without touching the wire-level
// code in the opamp-go client or server.
//
// Signing is performed over the raw bytes of a marshalled
// [protobufs.ServerToAgent] (the bytes carried in
// SignedServerToAgent.payload on the wire), producing a detached
// signature placed in SignedServerToAgent.signature. The receiver
// verifies the signature over the bytes exactly as they arrive on the
// wire — no re-marshalling is required, sidestepping protobuf's
// non-canonical-encoding caveat. See the Message Attestation section
// of the OpAMP specification for the wire protocol.
//
// The signing algorithm for a given connection is determined by the
// signing certificate's SignatureAlgorithm field; the OpAMP protocol
// does not negotiate algorithms.
//
// [GenerateCA] and [GenerateLeaf] are exported test helpers; they
// also serve smoke tests and the example server. Production
// deployments use externally-managed PKI and only need the
// [LocalSigner] / [LocalVerifier] constructors, the loader helpers,
// or a custom [Signer] / [Verifier] implementation.
package signing
