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
)

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
// Other RFC 5280 checks — per-certificate signature, validity window,
// basicConstraints, pathLenConstraint, critical extensions — are
// enforced by the underlying crypto/x509 implementation.
//
// Revocation checking via CRL/OCSP is RECOMMENDED by the OpAMP spec
// but not performed here in the current implementation; that is a
// follow-up. Operators MAY rely on short-lived signing certificates
// as a complementary mitigation.
func ValidateChain(_ context.Context, chainDER [][]byte, roots *x509.CertPool, now time.Time) (*x509.Certificate, error) {
	if len(chainDER) == 0 {
		return nil, ErrEmptyChain
	}
	if roots == nil {
		return nil, fmt.Errorf("%w: nil trust anchor pool", ErrChainValidation)
	}

	certs := make([]*x509.Certificate, 0, len(chainDER))
	for i, der := range chainDER {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("%w: chain[%d]: %v", ErrParseCertificate, i, err)
		}
		certs = append(certs, cert)
	}

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
	}

	if _, err := leaf.Verify(opts); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChainValidation, err)
	}

	return leaf, nil
}
