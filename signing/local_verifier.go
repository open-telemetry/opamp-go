package signing

import (
	"context"
	"crypto/x509"
	"errors"
	"time"
)

// ErrNilRoots is returned by NewLocalVerifier when roots is nil.
var ErrNilRoots = errors.New("signing: nil trust anchor pool")

// LocalVerifier is the in-process reference implementation of [Verifier],
// backed by a trust anchor pool. It is safe for concurrent use.
type LocalVerifier struct {
	roots *x509.CertPool
}

// NewLocalVerifier constructs a LocalVerifier over roots. The pool MUST be
// operator-managed and supplied out of band, never installed or modified by
// an OpAMP message.
func NewLocalVerifier(roots *x509.CertPool) (*LocalVerifier, error) {
	if roots == nil {
		return nil, ErrNilRoots
	}
	return &LocalVerifier{roots: roots}, nil
}

// ValidateChain implements [Verifier] using the package-level [ValidateChain].
func (v *LocalVerifier) ValidateChain(ctx context.Context, chainDER [][]byte, now time.Time, dnsName string) (*VerifiedCertificate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ValidateChain(ctx, chainDER, v.roots, now, dnsName)
}

// Verify implements [Verifier]. It returns ErrChainValidation if cert is no
// longer valid, ErrUnsupportedAlgorithm for an unsupported leaf key, and
// ErrSignatureMismatch if the signature does not verify.
func (v *LocalVerifier) Verify(ctx context.Context, payload, signature []byte, cert *VerifiedCertificate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cert == nil {
		return errors.New("signing: nil verified certificate")
	}
	if len(signature) == 0 {
		return errors.New("signing: empty signature")
	}
	if err := cert.ValidAt(time.Now()); err != nil {
		return err
	}
	return verifyWithCert(cert.Leaf(), payload, signature)
}
