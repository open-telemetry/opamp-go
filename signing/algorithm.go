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

// ErrUnsupportedAlgorithm indicates that a certificate's public key is
// not in the supported set: it is the wrong key type, an unsupported
// ECDSA curve, or an RSA key below rsaMinModulusBits.
var ErrUnsupportedAlgorithm = errors.New("signing: unsupported signature algorithm")

// algorithmFromCert derives the Algorithm to use for signature
// operations involving cert, dispatching on the leaf's own public key
// type and (for ECDSA) curve. This is the correct authority: the
// Algorithm controls how a payload is signed/verified, so it must match
// the leaf key's type and curve.
//
// cert.SignatureAlgorithm is deliberately NOT consulted. That field
// describes the algorithm the issuer used to sign this certificate,
// which is independent of the leaf key: a P-384 CA may legitimately
// issue a P-256 leaf, in which case cert.SignatureAlgorithm is
// ECDSAWithSHA384 even though the leaf signs payloads with P-256/SHA-256.
// The payload algorithm is fully determined by the leaf key returned
// here, so the issuer's signing algorithm is irrelevant and checking it
// would only reject valid cross-algorithm PKI hierarchies.
//
// Minimum RSA modulus is rsaMinModulusBits.
func algorithmFromCert(cert *x509.Certificate) (Algorithm, error) {
	switch pub := cert.PublicKey.(type) {
	case *ecdsa.PublicKey:
		switch pub.Curve {
		case elliptic.P256():
			return AlgorithmECDSAP256SHA256, nil
		case elliptic.P384():
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
		return AlgorithmRSAPKCS1v15SHA256, nil
	case ed25519.PublicKey:
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
