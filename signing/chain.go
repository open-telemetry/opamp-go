package signing

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for chain validation. Callers can use errors.Is to
// distinguish failure modes (for example, to log a structured reason
// for terminating a connection).
var (
	// ErrEmptyChain is returned when ValidateChain is called with an
	// empty chainDER slice. The OpAMP spec requires at least the leaf
	// signing certificate to be present.
	ErrEmptyChain = errors.New("signing: empty certificate chain")

	// ErrParseCertificate wraps an inner x509 parse failure on one of
	// the chain entries.
	ErrParseCertificate = errors.New("signing: parse certificate")

	// ErrChainValidation wraps an inner x509 path-validation failure
	// (expired cert, unknown issuer, missing EKU, etc.). The wrapped
	// error preserves the original x509-level reason.
	ErrChainValidation = errors.New("signing: chain validation")

	// ErrSignatureMismatch is returned when a detached signature does
	// not verify against the supplied public key and payload bytes.
	ErrSignatureMismatch = errors.New("signing: signature does not verify")

	// ErrServerNameRequired is returned when ValidateChain is called
	// with an empty dnsName. Hostname verification is mandatory: an
	// empty name would cause crypto/x509 to skip the SAN check
	// silently, so ValidateChain fails closed rather than accept a
	// chain that is not bound to the connected server.
	ErrServerNameRequired = errors.New("signing: server hostname required for chain validation")

	// ErrHostnameMismatch is returned when the leaf certificate's
	// Subject Alternative Name entries do not cover dnsName. This
	// prevents a certificate legitimately issued for one host from
	// being accepted for a connection to another.
	ErrHostnameMismatch = errors.New("signing: leaf certificate not valid for server hostname")
)

// VerifiedCertificate is a certificate chain that has passed path
// validation via [ValidateChain]. It is the only type [Verifier.Verify]
// accepts, so the compiler guarantees a signature is never verified
// against a chain that has not been validated — the unexported fields
// mean it cannot be constructed outside this package.
//
// It retains the validated chain, trust anchors, and hostname so that
// validity can be re-confirmed at time-of-use (see [VerifiedCertificate.ValidAt]),
// because a chain valid at handshake may expire before a later message.
//
// The zero value is unusable; obtain one from [ValidateChain].
type VerifiedCertificate struct {
	leaf    *x509.Certificate
	chain   []*x509.Certificate // ordered intermediates first, leaf last
	roots   *x509.CertPool
	dnsName string
}

// Leaf returns the validated leaf certificate (public key, SANs, EKU,
// etc.). Callers may inspect it but MUST treat it as read-only.
func (c *VerifiedCertificate) Leaf() *x509.Certificate { return c.leaf }

// ValidAt re-runs path validation of the chain as of now and returns a
// non-nil error if it is no longer valid — for example a certificate
// has expired. Because certificates expire (and may later be revoked),
// a chain validated at handshake is not trusted indefinitely: a
// verifier MUST call ValidAt at the moment it relies on the chain.
func (c *VerifiedCertificate) ValidAt(now time.Time) error {
	_, err := verifyParsedChain(c.chain, c.roots, now, c.dnsName)
	return err
}

// ValidateChain performs RFC 5280 §6 X.509 path validation of the
// supplied DER certificate chain against the trust anchor pool in
// roots.
//
// The chain MUST be ordered intermediates first, leaf last, matching
// the on-wire ordering of SignedServerToAgent.trust_chain_response.
// The root certificate (the Agent's pre-configured payload trust
// anchor) is supplied via roots and MUST NOT appear in chainDER.
//
// The leaf certificate MUST carry the id-kp-codeSigning Extended Key
// Usage (OID 1.3.6.1.5.5.7.3.3). This prevents certificates issued
// for TLS server authentication from being repurposed to sign OpAMP
// messages.
//
// The leaf's SAN must match dnsName (dNSName or iPAddress), binding the
// signing identity to the connected server. dnsName MUST be non-empty:
// an empty name makes crypto/x509 skip the SAN check, so ValidateChain
// fails closed with ErrServerNameRequired. Callers MUST NOT perform a
// separate hostname check.
//
// Other RFC 5280 checks — per-certificate signature, validity window,
// basicConstraints, pathLenConstraint, critical extensions — are
// enforced by the underlying crypto/x509 implementation.
//
// Revocation checking via CRL/OCSP is RECOMMENDED by the OpAMP spec
// but not performed here in the current implementation; that is a
// follow-up. Operators MAY rely on short-lived signing certificates
// as a complementary mitigation.
//
// On success it returns a [VerifiedCertificate]: the only type Verify
// accepts, so a signature can never be checked against a chain that
// has not passed validation. A validated chain is NOT trusted forever
// — certificates expire — so verifiers MUST re-confirm validity at
// time-of-use via [VerifiedCertificate.ValidAt] (Verify does this).
func ValidateChain(_ context.Context, chainDER [][]byte, roots *x509.CertPool, now time.Time, dnsName string) (*VerifiedCertificate, error) {
	if len(chainDER) == 0 {
		return nil, ErrEmptyChain
	}
	if roots == nil {
		return nil, fmt.Errorf("%w: nil trust anchor pool", ErrChainValidation)
	}
	if dnsName == "" {
		return nil, ErrServerNameRequired
	}

	certs := make([]*x509.Certificate, 0, len(chainDER))
	for i, der := range chainDER {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("%w: chain[%d]: %v", ErrParseCertificate, i, err)
		}
		certs = append(certs, cert)
	}

	leaf, err := verifyParsedChain(certs, roots, now, dnsName)
	if err != nil {
		return nil, err
	}
	return &VerifiedCertificate{leaf: leaf, chain: certs, roots: roots, dnsName: dnsName}, nil
}

// verifyParsedChain runs crypto/x509 path validation of an
// already-parsed chain (intermediates first, leaf last) and returns
// the validated leaf. Shared by ValidateChain and the time-of-use
// recheck in VerifiedCertificate.ValidAt.
func verifyParsedChain(certs []*x509.Certificate, roots *x509.CertPool, now time.Time, dnsName string) (*x509.Certificate, error) {
	leaf := certs[len(certs)-1]

	intermediates := x509.NewCertPool()
	for i := 0; i < len(certs)-1; i++ {
		intermediates.AddCert(certs[i])
	}

	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		DNSName:       dnsName,
	}

	if _, err := leaf.Verify(opts); err != nil {
		var hostErr x509.HostnameError
		if errors.As(err, &hostErr) {
			return nil, fmt.Errorf("%w: %v", ErrHostnameMismatch, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrChainValidation, err)
	}

	return leaf, nil
}
