package signing

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// ErrNilKey is returned by NewLocalSigner when key is nil.
var ErrNilKey = errors.New("signing: nil private key")

// LocalSigner is the in-process reference implementation of [Signer].
// It holds a private key and certificate chain in memory and signs
// requests synchronously without any network IO.
//
// LocalSigner is suitable for tests, the opamp-go example server, and
// any deployment where the signing private key is colocated with the
// OpAMP server process. Deployments that delegate signing to a hosted
// platform (HSM-backed RPC, central signing service) should provide
// their own Signer implementation; the wire-level opamp-go code is
// agnostic to which Signer is in use.
//
// LocalSigner is safe for concurrent use: the underlying crypto.Signer
// implementations in the Go standard library are themselves
// concurrency-safe.
type LocalSigner struct {
	key       crypto.Signer
	alg       Algorithm
	chainDER  [][]byte
	rootCADER []byte // set via WithRootCA; nil unless TOFU is supported
}

// NewLocalSigner constructs a LocalSigner from the supplied private
// key (typically a crypto.Signer implementation from the standard
// library) and certificate chain.
//
// The chain MUST be ordered intermediates first, leaf last; the leaf
// is the certificate whose private key signs payloads. The root MUST
// NOT be included — the Agent supplies the root via its pre-configured
// trust anchor pool.
//
// The signing algorithm is determined by the leaf certificate's public
// key type and (for ECDSA) curve, cross-checked against the cert's
// SignatureAlgorithm field. ErrUnsupportedAlgorithm is returned for
// any pubkey type/curve outside the supported baseline, for RSA keys
// below the minimum modulus (rsaMinModulusBits), or when
// SignatureAlgorithm does not match the leaf's actual key.
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

	chainDER := make([][]byte, len(chain))
	for i, cert := range chain {
		// cert.Raw is the DER bytes the certificate was parsed from
		// (or that x509.CreateCertificate produced). Copy to defend
		// against later mutation of cert.Raw by callers, even though
		// it's expected to be immutable in practice.
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

// Sign implements [Signer]. It signs the caller's bytes unchanged and
// returns them verbatim as SignResult.Payload: an in-process signer does not
// re-marshal, so the bytes signed are exactly the bytes passed in. The chain
// returned in SignResult.ChainDER is the one configured at construction. The
// context is honoured only for cancellation; the in-process signing operation
// itself does not block.
func (s *LocalSigner) Sign(ctx context.Context, payload []byte) (SignResult, error) {
	if err := ctx.Err(); err != nil {
		return SignResult{}, err
	}
	sig, err := signWithKey(s.key, s.alg, payload)
	if err != nil {
		return SignResult{}, err
	}
	// Return a shallow copy of the chain slice so callers cannot reorder or
	// replace the signer's internal slice elements. The DER backing arrays
	// are immutable after construction and are shared to keep Sign cheap on
	// the hot path.
	chain := make([][]byte, len(s.chainDER))
	copy(chain, s.chainDER)
	return SignResult{Payload: payload, Signature: sig, ChainDER: chain}, nil
}

// Algorithm reports the algorithm dispatched by this signer (derived
// from the leaf certificate). Exposed for diagnostics and tests.
func (s *LocalSigner) Algorithm() Algorithm {
	return s.alg
}

// WithRootCA attaches the root CA certificate to this signer, enabling
// [TrustAnchorProvider] support. The root CA is included in
// trust_chain_response.tofu_trust_anchor during TOFU enrollment so that
// Agents with no pre-configured trust anchor can bootstrap and persist it.
// Returns the receiver for chaining.
func (s *LocalSigner) WithRootCA(ca *x509.Certificate) *LocalSigner {
	der := make([]byte, len(ca.Raw))
	copy(der, ca.Raw)
	s.rootCADER = der
	return s
}

// TrustAnchorPEM implements [TrustAnchorProvider]. Returns the PEM-encoded
// root CA set by [WithRootCA]. Returns an error if WithRootCA was not called.
func (s *LocalSigner) TrustAnchorPEM(_ context.Context) ([]byte, error) {
	if len(s.rootCADER) == 0 {
		return nil, errors.New("signing: no root CA configured on LocalSigner (call WithRootCA first)")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.rootCADER}), nil
}
