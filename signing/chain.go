package signing

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for chain validation and signature verification.
var (
	// ErrEmptyChain is returned for an empty chain; at least the leaf is required.
	ErrEmptyChain = errors.New("signing: empty certificate chain")

	// ErrParseCertificate wraps a failure to parse a chain entry.
	ErrParseCertificate = errors.New("signing: parse certificate")

	// ErrChainValidation wraps a path-validation failure (expired, unknown
	// issuer, missing EKU, ...), preserving the x509 reason.
	ErrChainValidation = errors.New("signing: chain validation")

	// ErrSignatureMismatch is returned when a signature does not verify.
	ErrSignatureMismatch = errors.New("signing: signature does not verify")

	// ErrServerNameRequired is returned for an empty dnsName: crypto/x509
	// would silently skip the SAN check, so validation fails closed.
	ErrServerNameRequired = errors.New("signing: server hostname required for chain validation")

	// ErrHostnameMismatch is returned when the leaf's SANs do not cover dnsName.
	ErrHostnameMismatch = errors.New("signing: leaf certificate not valid for server hostname")
)

// VerifiedCertificate is a chain that has passed [ValidateChain]. Its fields
// are unexported, so it can only come from validation, and it is the only
// type [Verifier.Verify] accepts: a signature is never checked against an
// unvalidated chain. It retains what is needed to re-check validity at
// time of use ([VerifiedCertificate.ValidAt]).
type VerifiedCertificate struct {
	leaf    *x509.Certificate
	chain   []*x509.Certificate // ordered intermediates first, leaf last
	roots   *x509.CertPool
	dnsName string
}

// Leaf returns the validated leaf certificate. Callers MUST treat it as read-only.
func (c *VerifiedCertificate) Leaf() *x509.Certificate { return c.leaf }

// ValidAt re-runs path validation as of now, failing if the chain is no
// longer valid (for example, a certificate expired). Verifiers MUST call it
// when they rely on the chain.
func (c *VerifiedCertificate) ValidAt(now time.Time) error {
	_, err := verifyParsedChain(c.chain, c.roots, now, c.dnsName)
	return err
}

// ValidateChain performs RFC 5280 path validation of chainDER against roots.
//
// chainDER is ordered intermediates first, leaf last, as in
// trust_chain_response; the root is supplied via roots and MUST NOT appear
// in it. The leaf MUST carry the id-kp-codeSigning EKU, so TLS server
// certificates cannot be repurposed, and its SANs (dNSName or iPAddress)
// must match dnsName, which MUST be non-empty. Other RFC 5280 checks are
// enforced by crypto/x509. Revocation is not checked; the spec relies on
// short-lived signing certificates instead.
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

// verifyParsedChain validates a parsed chain (intermediates first, leaf
// last) and returns the leaf.
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
