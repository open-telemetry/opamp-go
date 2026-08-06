package signing

import (
	"context"
	"time"
)

// Algorithm identifies the signature algorithm used by a signing
// certificate. The OpAMP protocol does not negotiate algorithms; the
// algorithm in use is determined by the certificate's SignatureAlgorithm
// field. This enum exists so that test helpers, cert generators, and
// internal dispatch tables can refer to a specific algorithm by name.
//
// FIPS 140-3 note: the three classical algorithms below (ECDSA P-256 and
// P-384 with SHA-2, and RSA PKCS#1 v1.5 with SHA-256) are Approved for
// use in FIPS 140-3 validated cryptographic modules (ECDSA per FIPS
// 186-4/186-5 with NIST curves from SP 800-186; RSASSA-PKCS1-v1_5 with a
// ≥2048-bit modulus; SHA-2 per FIPS 180-4). Ed25519 (EdDSA) was added as
// an Approved algorithm only in FIPS 186-5 (2023) and its availability in
// FIPS 140-3 *validated* modules still lags — deployments with a FIPS
// requirement should confirm their module supports it, or restrict
// signers to the ECDSA/RSA options.
type Algorithm uint8

const (
	// AlgorithmUnspecified is the zero value and is never a valid
	// algorithm in production.
	AlgorithmUnspecified Algorithm = iota
	// AlgorithmECDSAP256SHA256 — ECDSA over the P-256 curve with
	// SHA-256, DER-encoded (r,s) signatures. FIPS 140-3 Approved.
	AlgorithmECDSAP256SHA256
	// AlgorithmECDSAP384SHA384 — ECDSA over the P-384 curve with
	// SHA-384, DER-encoded (r,s) signatures. FIPS 140-3 Approved.
	AlgorithmECDSAP384SHA384
	// AlgorithmRSAPKCS1v15SHA256 — RSA with PKCS#1 v1.5 padding and
	// SHA-256. Minimum 2048-bit modulus recommended. FIPS 140-3 Approved.
	AlgorithmRSAPKCS1v15SHA256
	// AlgorithmEd25519 — Ed25519 (signs the payload directly; no
	// pre-hash). Approved in FIPS 186-5, but validated-module support is
	// limited; not guaranteed available in a FIPS 140-3 deployment.
	AlgorithmEd25519
)

// String returns the canonical name of the algorithm.
func (a Algorithm) String() string {
	switch a {
	case AlgorithmECDSAP256SHA256:
		return "ECDSA-P256-SHA256"
	case AlgorithmECDSAP384SHA384:
		return "ECDSA-P384-SHA384"
	case AlgorithmRSAPKCS1v15SHA256:
		return "RSA-PKCS1v15-SHA256"
	case AlgorithmEd25519:
		return "Ed25519"
	default:
		return "unspecified"
	}
}

// SignResult is the output of one signing operation. Bundling all three
// fields in a single return guarantees the chain anchors the certificate that
// produced the signature — a separate chain lookup could race a rotation and
// return a chain for a different certificate.
//
// It is also the extension point for [Signer]: future metadata (a certificate
// identifier, the leaf's NotAfter for proactive rotation, the algorithm) is
// added as a field without changing the Sign signature.
//
// All fields are read-only: callers MUST NOT modify the returned slices or
// their backing bytes. Implementations may return storage shared with the
// signer (see [LocalSigner]), so mutation could corrupt subsequent signatures.
type SignResult struct {
	// Payload is the exact bytes that were signed, transmitted as
	// SignedServerToAgent.payload. It is returned (rather than reusing the
	// bytes passed to Sign) because protobuf serialization is not canonical: a
	// signer that re-marshals server-side produces different bytes, and the
	// Agent verifies over the bytes it receives. An in-process signer returns
	// the input unchanged.
	Payload []byte

	// Signature is the detached signature over Payload, transmitted as
	// SignedServerToAgent.signature.
	Signature []byte

	// ChainDER is the signing certificate chain in DER form, ordered first
	// intermediate to signing leaf. The root (already held by the Agent as its
	// payload trust anchor) is excluded.
	ChainDER [][]byte
}

// Signer produces a detached signature over payload bytes together with the
// certificate chain that anchors it (see [SignResult]).
//
// Implementations may sign in-process (see [LocalSigner]) or delegate to an
// external service (HSM, remote signing RPC). Sign takes a context for
// cancellation, deadlines, and trace propagation.
type Signer interface {
	// Sign computes a detached signature over payload and returns it with the
	// signed bytes and current chain as a SignResult. The algorithm is
	// determined by the signing certificate, not passed by the caller.
	//
	// There is no separate pre-flight or chain-fetch step: a misconfigured or
	// unavailable signer fails here (the server then closes the connection).
	// This lets signers that mint a certificate only as a side effect of
	// signing — with no "current certificate" to report ahead of time —
	// implement the interface naturally.
	Sign(ctx context.Context, payload []byte) (SignResult, error)
}

// TrustAnchorProvider is an optional interface a [Signer] may satisfy when it
// also holds the root CA. The server type-asserts for it to populate
// trust_chain_response.tofu_trust_anchor during TOFU enrollment. Signers that
// only expose the leaf chain (e.g. a remote HSM) need not implement it; TOFU
// enrollment is then unavailable for that deployment.
type TrustAnchorProvider interface {
	// TrustAnchorPEM returns the PEM-encoded root CA certificate that Agents
	// should use as their payload trust anchor.
	TrustAnchorPEM(ctx context.Context) ([]byte, error)
}

// Verifier validates a delivered trust chain and verifies detached
// signatures against the resulting leaf certificate.
//
// Implementations are expected to perform RFC 5280 §6 X.509 path
// validation in ValidateChain and re-confirm the chain is still valid
// at time-of-use in Verify (see [VerifiedCertificate]).
type Verifier interface {
	// ValidateChain performs RFC 5280 §6 path validation of the
	// supplied DER certificate chain against the verifier's
	// pre-configured trust anchor pool. The chain MUST be ordered
	// intermediates first, leaf last; the root is supplied via the
	// verifier's configuration and MUST NOT appear in chainDER.
	//
	// The leaf's SAN must match dnsName, binding the signing identity
	// to the connected server. dnsName MUST be non-empty; implementations
	// fail closed otherwise. Callers MUST NOT perform a separate
	// hostname check.
	//
	// On success it returns a [VerifiedCertificate], which the Agent
	// stores for the duration of the connection and passes to Verify on
	// every subsequent message.
	ValidateChain(ctx context.Context, chainDER [][]byte, now time.Time, dnsName string) (*VerifiedCertificate, error)

	// Verify validates signature over payload using the public key of
	// cert's leaf. Because a chain valid at ValidateChain time may have
	// expired since, Verify MUST first re-confirm cert is still valid at
	// the current time (see [VerifiedCertificate.ValidAt]) and reject the
	// message otherwise. The signature algorithm is derived from the
	// leaf's public-key type and (for ECDSA) curve, cross-checked against
	// its SignatureAlgorithm. The payload bytes are the wire bytes of
	// SignedServerToAgent.payload — the receiver does not re-marshal
	// anything.
	Verify(ctx context.Context, payload, signature []byte, cert *VerifiedCertificate) error
}
