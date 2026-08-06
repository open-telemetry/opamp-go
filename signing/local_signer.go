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

// ErrKeyMismatch is returned by NewLocalSigner when the private key's
// public key does not match the leaf certificate's public key. Such a
// signer would produce signatures no Agent can verify, so it is
// rejected at construction rather than failing on every connection.
var ErrKeyMismatch = errors.New("signing: private key does not match leaf certificate public key")

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
	rootCAPEM []byte // PEM-encoded, set via WithRootCA; nil unless TOFU is supported
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
//
// The material is validated at construction so misconfiguration fails
// at startup rather than as opaque client-side errors later: key's
// public key MUST match the leaf (ErrKeyMismatch), and the chain MUST
// be internally consistent (ErrChainValidation; see
// verifyChainInternally).
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

// verifyChainInternally checks that the chain is well-formed: correct
// order, each certificate issued by the next, leaf carrying
// id-kp-codeSigning, and all valid at now. The top-most
// supplied certificate is the trust anchor, since a signing chain
// excludes the real root by design (the Agent holds it) — so this does
// NOT prove the chain terminates at the Agent's trust anchor.
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
	// Return a fresh slice header so a caller cannot reassign or reorder the
	// signer's internal chain entries. The DER backing arrays are shared, not
	// copied: they are immutable after construction, and per [SignResult] the
	// caller must treat ChainDER as read-only.
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
// Returns the receiver for chaining. The root CA is PEM-encoded once here
// rather than on every TrustAnchorPEM call.
func (s *LocalSigner) WithRootCA(ca *x509.Certificate) *LocalSigner {
	s.rootCAPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	return s
}

// TrustAnchorPEM implements [TrustAnchorProvider]. Returns the PEM-encoded
// root CA set by [WithRootCA]. Returns an error if WithRootCA was not called.
func (s *LocalSigner) TrustAnchorPEM(_ context.Context) ([]byte, error) {
	if len(s.rootCAPEM) == 0 {
		return nil, errors.New("signing: no root CA configured on LocalSigner (call WithRootCA first)")
	}
	// Return a copy so the caller cannot mutate the signer's stored PEM.
	out := make([]byte, len(s.rootCAPEM))
	copy(out, s.rootCAPEM)
	return out, nil
}
