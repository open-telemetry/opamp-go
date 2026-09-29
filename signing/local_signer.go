package signing

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// ErrNilKey is returned by NewLocalSigner when key is nil.
var ErrNilKey = errors.New("signing: nil private key")

// ErrKeyMismatch is returned by NewLocalSigner when the private key does
// not match the leaf certificate's public key.
var ErrKeyMismatch = errors.New("signing: private key does not match leaf certificate public key")

// LocalSigner is the in-process reference implementation of [Signer]: it
// signs with a crypto.Signer and a fixed certificate chain. The key may be
// in memory or backed by hardware or a KMS. It is safe for concurrent use
// if the crypto.Signer is.
type LocalSigner struct {
	key       crypto.Signer
	alg       Algorithm
	chainDER  [][]byte
	rootCAPEM []byte // PEM-encoded, set via WithRootCA; nil unless TOFU is supported
}

// NewLocalSigner constructs a LocalSigner from key and chain (intermediates
// first, leaf last, root excluded). The algorithm is derived from the leaf's
// public key.
//
// The material is validated up front so misconfiguration fails at startup:
// ErrUnsupportedAlgorithm for an unsupported leaf key, ErrKeyMismatch if key
// does not match the leaf, and ErrChainValidation if the chain is not
// internally consistent.
func NewLocalSigner(key crypto.Signer, chain []*x509.Certificate) (*LocalSigner, error) {
	if key == nil {
		return nil, ErrNilKey
	}
	if len(chain) == 0 {
		return nil, ErrEmptyChain
	}
	leaf := chain[len(chain)-1]
	alg, err := algorithmFromCert(leaf)
	if err != nil {
		return nil, err
	}

	// The private key MUST correspond to the leaf. All supported public
	// key types implement Equal(crypto.PublicKey) bool (Go stdlib).
	type publicKeyEqual interface{ Equal(crypto.PublicKey) bool }
	pub, ok := key.Public().(publicKeyEqual)
	if !ok || !pub.Equal(leaf.PublicKey) {
		return nil, ErrKeyMismatch
	}

	if err := verifyChainInternally(chain, time.Now()); err != nil {
		return nil, err
	}

	chainDER := make([][]byte, len(chain))
	for i, cert := range chain {
		// Copy so later mutation of cert.Raw cannot affect the signer.
		raw := make([]byte, len(cert.Raw))
		copy(raw, cert.Raw)
		chainDER[i] = raw
	}

	return &LocalSigner{
		key:      key,
		alg:      alg,
		chainDER: chainDER,
	}, nil
}

// verifyChainInternally checks the chain is well-formed (ordered, each
// certificate issuing the next, leaf carrying id-kp-codeSigning, all valid
// at now), treating its top certificate as the anchor. It cannot prove the
// chain reaches the Agent's trust anchor, which the signer never holds.
func verifyChainInternally(chain []*x509.Certificate, now time.Time) error {
	leaf := chain[len(chain)-1]
	roots := x509.NewCertPool()
	roots.AddCert(chain[0])
	intermediates := x509.NewCertPool()
	for i := 1; i < len(chain)-1; i++ {
		intermediates.AddCert(chain[i])
	}
	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("%w: %v", ErrChainValidation, err)
	}
	return nil
}

// Sign implements [Signer]. It signs payload as-is, so SignResult.Payload is
// the input, and returns the chain configured at construction.
func (s *LocalSigner) Sign(ctx context.Context, payload []byte) (SignResult, error) {
	if err := ctx.Err(); err != nil {
		return SignResult{}, err
	}
	sig, err := signWithKey(s.key, s.alg, payload)
	if err != nil {
		return SignResult{}, err
	}
	// Copy the outer slice only; the DER bytes are shared and read-only.
	chain := make([][]byte, len(s.chainDER))
	copy(chain, s.chainDER)
	return SignResult{Payload: payload, Signature: sig, ChainDER: chain}, nil
}

// Algorithm reports the signer's algorithm, derived from the leaf certificate.
func (s *LocalSigner) Algorithm() Algorithm {
	return s.alg
}

// WithRootCA attaches the root CA, enabling [TrustAnchorProvider] so the
// server can offer it to Agents performing TOFU enrollment. It returns the
// receiver for chaining.
func (s *LocalSigner) WithRootCA(ca *x509.Certificate) *LocalSigner {
	s.rootCAPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	return s
}

// TrustAnchorPEM implements [TrustAnchorProvider], returning the root CA set
// by [LocalSigner.WithRootCA], or an error if none was set.
func (s *LocalSigner) TrustAnchorPEM(_ context.Context) ([]byte, error) {
	if len(s.rootCAPEM) == 0 {
		return nil, errors.New("signing: no root CA configured on LocalSigner (call WithRootCA first)")
	}
	out := make([]byte, len(s.rootCAPEM))
	copy(out, s.rootCAPEM)
	return out, nil
}
