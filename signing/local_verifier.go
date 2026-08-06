package signing

import (
	"context"
	"crypto/x509"
	"errors"
	"time"
)

// ErrNilRoots is returned by NewLocalVerifier when roots is nil.
var ErrNilRoots = errors.New("signing: nil trust anchor pool")

// LocalVerifier is the in-process reference implementation of
// [Verifier]. It wraps a trust anchor pool and uses [ValidateChain]
// for path validation plus the algorithm-dispatch table in
// algorithm.go for signature verification.
//
// LocalVerifier is safe for concurrent use.
type LocalVerifier struct {
	roots *x509.CertPool
}

// NewLocalVerifier constructs a LocalVerifier that will validate
// delivered certificate chains against the supplied trust anchor pool.
//
// The trust anchor pool MUST be operator-managed and supplied
// out-of-band (typically a PEM file path read at startup); it MUST NOT
// be installed or modified by any OpAMP message.
func NewLocalVerifier(roots *x509.CertPool) (*LocalVerifier, error) {
	if roots == nil {
		return nil, ErrNilRoots
	}
	return &LocalVerifier{roots: roots}, nil
}

// ValidateChain implements [Verifier], delegating to the package-level
// [ValidateChain] function with the verifier's trust anchor pool.
func (v *LocalVerifier) ValidateChain(ctx context.Context, chainDER [][]byte, now time.Time, dnsName string) (*VerifiedCertificate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ValidateChain(ctx, chainDER, v.roots, now, dnsName)
}

// Verify implements [Verifier]. It first re-confirms cert is still
// valid at the current time — a chain validated at handshake may have
// expired since — and rejects the message if not. It then checks the
// signature: the algorithm is derived from the leaf's public-key type
// and (for ECDSA) curve, cross-checked against its SignatureAlgorithm.
// ErrUnsupportedAlgorithm is returned for any pubkey type/curve outside
// the supported baseline (or when SignatureAlgorithm disagrees with the
// actual key); ErrChainValidation when the chain is no longer valid;
// ErrSignatureMismatch when the signature does not verify.
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
	// Re-establish trust at time-of-use: the chain validated at the
	// handshake is not trusted indefinitely (certificates expire).
	if err := cert.ValidAt(time.Now()); err != nil {
		return err
	}
	leaf := cert.Leaf()
	alg, err := algorithmFromCert(leaf)
	if err != nil {
		return err
	}
	return verifyWithPub(leaf.PublicKey, alg, payload, signature)
}
