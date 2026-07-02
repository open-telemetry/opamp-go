package signing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"errors"
	"fmt"
)

// rsaMinModulusBits is the minimum acceptable RSA modulus size. Keys
// below this size are rejected even if the rest of the chain validates.
const rsaMinModulusBits = 2048

// ErrUnsupportedAlgorithm indicates that a certificate's public key
// (or the algorithm declared by the issuer's signature on the cert)
// is not in the supported set: it is the wrong key type, an
// unsupported ECDSA curve, an RSA key below rsaMinModulusBits, or the
// declared SignatureAlgorithm does not match the leaf's actual key
// type/curve.
var ErrUnsupportedAlgorithm = errors.New("signing: unsupported signature algorithm")

// algorithmFromCert derives the Algorithm to use for signature
// operations involving cert. Dispatching on the leaf's own public key
// type (rather than on cert.SignatureAlgorithm, which describes the
// issuer's signing of the cert itself) is the correct authority: the
// Algorithm controls how a payload is signed/verified, and that has to
// match the leaf key's algorithm and curve, not the issuer's.
//
// The function additionally cross-checks cert.SignatureAlgorithm
// against the leaf key so that a certificate whose declared algorithm
// is inconsistent with its pubkey is rejected up front. This prevents
// a within-family mismatch (e.g., a P-384 CA issuing a P-256 leaf with
// SignatureAlgorithm=ECDSAWithSHA384) from silently accepting the
// wrong hash size at sign/verify time.
//
// Minimum RSA modulus is rsaMinModulusBits.
func algorithmFromCert(cert *x509.Certificate) (Algorithm, error) {
	switch pub := cert.PublicKey.(type) {
	case *ecdsa.PublicKey:
		switch pub.Curve {
		case elliptic.P256():
			if cert.SignatureAlgorithm != x509.ECDSAWithSHA256 {
				return AlgorithmUnspecified, fmt.Errorf("%w: P-256 leaf with mismatched declared algorithm %s",
					ErrUnsupportedAlgorithm, cert.SignatureAlgorithm)
			}
			return AlgorithmECDSAP256SHA256, nil
		case elliptic.P384():
			if cert.SignatureAlgorithm != x509.ECDSAWithSHA384 {
				return AlgorithmUnspecified, fmt.Errorf("%w: P-384 leaf with mismatched declared algorithm %s",
					ErrUnsupportedAlgorithm, cert.SignatureAlgorithm)
			}
			return AlgorithmECDSAP384SHA384, nil
		default:
			curveName := "unknown"
			if pub.Curve != nil && pub.Curve.Params() != nil {
				curveName = pub.Curve.Params().Name
			}
			return AlgorithmUnspecified, fmt.Errorf("%w: unsupported ECDSA curve %s",
				ErrUnsupportedAlgorithm, curveName)
		}
	case *rsa.PublicKey:
		if pub.N == nil || pub.N.BitLen() < rsaMinModulusBits {
			bits := 0
			if pub.N != nil {
				bits = pub.N.BitLen()
			}
			return AlgorithmUnspecified, fmt.Errorf("%w: RSA key %d bits < %d",
				ErrUnsupportedAlgorithm, bits, rsaMinModulusBits)
		}
		if cert.SignatureAlgorithm != x509.SHA256WithRSA {
			return AlgorithmUnspecified, fmt.Errorf("%w: RSA leaf with mismatched declared algorithm %s",
				ErrUnsupportedAlgorithm, cert.SignatureAlgorithm)
		}
		return AlgorithmRSAPKCS1v15SHA256, nil
	case ed25519.PublicKey:
		if cert.SignatureAlgorithm != x509.PureEd25519 {
			return AlgorithmUnspecified, fmt.Errorf("%w: Ed25519 leaf with mismatched declared algorithm %s",
				ErrUnsupportedAlgorithm, cert.SignatureAlgorithm)
		}
		return AlgorithmEd25519, nil
	default:
		return AlgorithmUnspecified, fmt.Errorf("%w: unsupported public key type %T",
			ErrUnsupportedAlgorithm, pub)
	}
}

// signWithKey produces a detached signature over payload using key,
// dispatching on alg. The caller is responsible for matching alg to
// the type of key (private key types are not switchable at runtime).
func signWithKey(key crypto.Signer, alg Algorithm, payload []byte) ([]byte, error) {
	switch alg {
	case AlgorithmECDSAP256SHA256:
		k, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: ECDSA-P256 requires *ecdsa.PrivateKey, got %T", ErrUnsupportedAlgorithm, key)
		}
		h := sha256.Sum256(payload)
		return ecdsa.SignASN1(rand.Reader, k, h[:])

	case AlgorithmECDSAP384SHA384:
		k, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: ECDSA-P384 requires *ecdsa.PrivateKey, got %T", ErrUnsupportedAlgorithm, key)
		}
		h := sha512.Sum384(payload)
		return ecdsa.SignASN1(rand.Reader, k, h[:])

	case AlgorithmRSAPKCS1v15SHA256:
		k, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: RSA-PKCS1v15-SHA256 requires *rsa.PrivateKey, got %T", ErrUnsupportedAlgorithm, key)
		}
		h := sha256.Sum256(payload)
		return rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, h[:])

	case AlgorithmEd25519:
		k, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: Ed25519 requires ed25519.PrivateKey, got %T", ErrUnsupportedAlgorithm, key)
		}
		return ed25519.Sign(k, payload), nil

	default:
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedAlgorithm, alg)
	}
}

// verifyWithPub verifies signature over payload using the supplied
// public key under alg. Returns ErrSignatureMismatch when the
// signature does not verify, or ErrUnsupportedAlgorithm if alg or pub
// is unsupported.
func verifyWithPub(pub crypto.PublicKey, alg Algorithm, payload, signature []byte) error {
	switch alg {
	case AlgorithmECDSAP256SHA256:
		p, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("%w: ECDSA-P256 requires *ecdsa.PublicKey, got %T", ErrUnsupportedAlgorithm, pub)
		}
		h := sha256.Sum256(payload)
		if !ecdsa.VerifyASN1(p, h[:], signature) {
			return ErrSignatureMismatch
		}
		return nil

	case AlgorithmECDSAP384SHA384:
		p, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("%w: ECDSA-P384 requires *ecdsa.PublicKey, got %T", ErrUnsupportedAlgorithm, pub)
		}
		h := sha512.Sum384(payload)
		if !ecdsa.VerifyASN1(p, h[:], signature) {
			return ErrSignatureMismatch
		}
		return nil

	case AlgorithmRSAPKCS1v15SHA256:
		p, ok := pub.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("%w: RSA-PKCS1v15-SHA256 requires *rsa.PublicKey, got %T", ErrUnsupportedAlgorithm, pub)
		}
		h := sha256.Sum256(payload)
		if err := rsa.VerifyPKCS1v15(p, crypto.SHA256, h[:], signature); err != nil {
			return fmt.Errorf("%w: %v", ErrSignatureMismatch, err)
		}
		return nil

	case AlgorithmEd25519:
		p, ok := pub.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("%w: Ed25519 requires ed25519.PublicKey, got %T", ErrUnsupportedAlgorithm, pub)
		}
		if !ed25519.Verify(p, payload, signature) {
			return ErrSignatureMismatch
		}
		return nil

	default:
		return fmt.Errorf("%w: %d", ErrUnsupportedAlgorithm, alg)
	}
}
