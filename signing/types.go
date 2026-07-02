package signing

import (
	"context"
	"crypto/x509"
	"time"
)

// Algorithm identifies the signature algorithm used by a signing
// certificate. The OpAMP protocol does not negotiate algorithms; the
// algorithm in use is determined by the certificate's SignatureAlgorithm
// field. This enum exists so that test helpers, cert generators, and
// internal dispatch tables can refer to a specific algorithm by name.
type Algorithm uint8

const (
	// AlgorithmUnspecified is the zero value and is never a valid
	// algorithm in production.
	AlgorithmUnspecified Algorithm = iota
	// AlgorithmECDSAP256SHA256 — ECDSA over the P-256 curve with
	// SHA-256, DER-encoded (r,s) signatures.
	AlgorithmECDSAP256SHA256
	// AlgorithmECDSAP384SHA384 — ECDSA over the P-384 curve with
	// SHA-384, DER-encoded (r,s) signatures.
	AlgorithmECDSAP384SHA384
	// AlgorithmRSAPKCS1v15SHA256 — RSA with PKCS#1 v1.5 padding and
	// SHA-256. Minimum 2048-bit modulus recommended.
	AlgorithmRSAPKCS1v15SHA256
	// AlgorithmEd25519 — Ed25519 (signs the payload directly; no
	// pre-hash).
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

// Signer produces detached signatures over arbitrary payload bytes and
// supplies the signing certificate chain.
//
// Implementations may sign locally with an in-process key (see
// [LocalSigner]) or delegate to an external signing service (HSM,
// remote signing RPC, hosted platforms with policy gating). Sign and
// ChainDER both accept a context so RPC-backed implementations can
// cancel, set deadlines, and propagate trace IDs.
type Signer interface {
	// Sign computes a signature over payload. The OpAMP server places
	// the returned bytes into SignedServerToAgent.signature on the
	// wire. The signing algorithm is determined by the signing
	// certificate; the caller does not pass it explicitly.
	Sign(ctx context.Context, payload []byte) ([]byte, error)

	// ChainDER returns the signing certificate chain in DER form,
	// ordered from the first intermediate down to the signing leaf.
	// The root certificate (which the Agent already possesses as its
	// pre-configured payload trust anchor) is excluded.
	//
	// The OpAMP server snapshots this once per new client connection
	// and reuses the result for the connection's lifetime so that
	// mid-session rotation on the signer side does not change the
	// chain mid-stream.
	ChainDER(ctx context.Context) ([][]byte, error)
}

// TrustAnchorProvider is an optional interface that [Signer] implementations
// may satisfy when they also hold the root CA certificate. The OpAMP server
// checks for this interface (via type assertion) to populate
// trust_chain_response.tofu_trust_anchor during TOFU enrollment.
// Signers that do not hold the root CA (for example, a remote HSM-backed
// signer that only exposes the leaf chain) need not implement this interface;
// TOFU enrollment will simply not be available for those deployments.
type TrustAnchorProvider interface {
	// TrustAnchorPEM returns the PEM-encoded root CA certificate that Agents
	// should use as their payload trust anchor.
	TrustAnchorPEM(ctx context.Context) ([]byte, error)
}

// Verifier validates a delivered trust chain and verifies detached
// signatures against the resulting leaf certificate.
//
// Implementations are expected to perform RFC 5280 §6 X.509 path
// validation in ValidateChain. The Verify method performs the
// signature-only check against the leaf returned by a successful
// ValidateChain call.
type Verifier interface {
	// ValidateChain performs RFC 5280 §6 path validation of the
	// supplied DER certificate chain against the verifier's
	// pre-configured trust anchor pool. The chain MUST be ordered
	// intermediates first, leaf last; the root is supplied via the
	// verifier's configuration and MUST NOT appear in chainDER.
	//
	// Returns the validated leaf certificate on success. The Agent
	// stores the leaf for the duration of the connection and passes
	// it to Verify on every subsequent message.
	ValidateChain(ctx context.Context, chainDER [][]byte, now time.Time) (*x509.Certificate, error)

	// Verify validates signature over payload using the public key of
	// leaf. The signature algorithm is derived from leaf's public-key
	// type and (for ECDSA) curve, cross-checked against
	// leaf.SignatureAlgorithm. The payload bytes are the wire bytes of
	// SignedServerToAgent.payload — the receiver does not re-marshal
	// anything.
	Verify(ctx context.Context, payload, signature []byte, leaf *x509.Certificate) error
}
