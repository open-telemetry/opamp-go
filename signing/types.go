package signing

import (
	"context"
	"time"
)

// Algorithm identifies a payload signature algorithm. It is determined by
// the signing leaf's public key, never negotiated.
//
// The ECDSA and RSA algorithms are FIPS 140-3 Approved. Ed25519 is
// Approved only as of FIPS 186-5 and may be missing from validated
// modules; FIPS deployments should confirm support or avoid it.
type Algorithm uint8

const (
	// AlgorithmUnspecified is the zero value; never valid.
	AlgorithmUnspecified Algorithm = iota
	// AlgorithmECDSAP256SHA256 is ECDSA P-256 with SHA-256 (ASN.1 DER signatures).
	AlgorithmECDSAP256SHA256
	// AlgorithmECDSAP384SHA384 is ECDSA P-384 with SHA-384 (ASN.1 DER signatures).
	AlgorithmECDSAP384SHA384
	// AlgorithmRSAPKCS1v15SHA256 is RSA PKCS#1 v1.5 with SHA-256; modulus of at least 2048 bits.
	AlgorithmRSAPKCS1v15SHA256
	// AlgorithmEd25519 is Ed25519, signing the payload directly.
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

// SignResult is the output of one signing operation. Returning all three
// fields together guarantees the chain anchors the certificate that produced
// the signature; a separate chain lookup could race a rotation. New metadata
// can be added as fields without changing [Signer].
//
// Callers MUST treat all fields as read-only: implementations may share the
// underlying storage (see [LocalSigner]).
type SignResult struct {
	// Payload is the exact bytes signed, transmitted as
	// SignedServerToAgent.payload. A signer that re-marshals produces bytes
	// different from its input, and the Agent verifies what it receives.
	Payload []byte

	// Signature is the detached signature over Payload, transmitted as
	// SignedServerToAgent.signature.
	Signature []byte

	// ChainDER is the certificate chain in DER form, intermediates first and
	// signing leaf last. The root (the Agent's trust anchor) is excluded.
	ChainDER [][]byte
}

// Signer produces a detached signature over payload bytes together with the
// certificate chain that anchors it. Implementations may sign in-process
// ([LocalSigner]) or delegate to an external service.
type Signer interface {
	// Sign signs payload and returns the signed bytes, signature, and chain.
	// The algorithm is determined by the signing certificate. There is no
	// separate chain-fetch step, so signers that learn their certificate
	// only by signing fit naturally; a failing signer errors here and the
	// server closes the connection.
	Sign(ctx context.Context, payload []byte) (SignResult, error)
}

// TrustAnchorProvider is an optional interface a [Signer] may implement to
// supply the root CA for trust_chain_response.tofu_trust_anchor. Without it,
// TOFU enrollment is unavailable.
type TrustAnchorProvider interface {
	// TrustAnchorPEM returns the PEM-encoded root CA certificate.
	TrustAnchorPEM(ctx context.Context) ([]byte, error)
}

// Verifier validates a delivered trust chain and verifies detached
// signatures against its leaf.
type Verifier interface {
	// ValidateChain performs RFC 5280 path validation of chainDER
	// (intermediates first, leaf last, root excluded) against the
	// verifier's trust anchors.
	//
	// The leaf's SANs must match dnsName, the host the Agent connected to.
	// dnsName MUST be non-empty (implementations fail closed otherwise) and
	// callers MUST NOT perform a separate hostname check. A leaf may list
	// several hosts (for example a gateway and the origin server); the
	// match against that set is never relaxed, and a host outside it fails
	// with ErrHostnameMismatch.
	//
	// The returned [VerifiedCertificate] is passed to Verify for each
	// message on the connection.
	ValidateChain(ctx context.Context, chainDER [][]byte, now time.Time, dnsName string) (*VerifiedCertificate, error)

	// Verify checks signature over payload (the wire bytes of
	// SignedServerToAgent.payload) with cert's leaf. It MUST first confirm
	// cert is still valid now (see [VerifiedCertificate.ValidAt]), since
	// the chain may have expired after ValidateChain.
	Verify(ctx context.Context, payload, signature []byte, cert *VerifiedCertificate) error
}
