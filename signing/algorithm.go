package signing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
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

// algorithmParams maps each supported Algorithm to its payload digest
// (zero for Ed25519, which signs the payload directly) and the matching
// x509.SignatureAlgorithm used for verification.
var algorithmParams = map[Algorithm]struct {
	hash   crypto.Hash
	sigAlg x509.SignatureAlgorithm
}{
	AlgorithmECDSAP256SHA256:   {crypto.SHA256, x509.ECDSAWithSHA256},
	AlgorithmECDSAP384SHA384:   {crypto.SHA384, x509.ECDSAWithSHA384},
	AlgorithmRSAPKCS1v15SHA256: {crypto.SHA256, x509.SHA256WithRSA},
	AlgorithmEd25519:           {0, x509.PureEd25519},
}

// algorithmFromCert derives the payload signature Algorithm from the
// leaf's own public key type and (for ECDSA) curve.
//
// cert.SignatureAlgorithm is deliberately not consulted: it describes how
// the issuer signed this certificate, which is independent of the leaf
// key (a P-384 CA may issue a P-256 leaf).
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

// signWithKey produces a detached signature over payload under alg. It uses
// only the crypto.Signer interface, so hardware- or KMS-backed keys work as
// well as in-memory standard library keys.
func signWithKey(key crypto.Signer, alg Algorithm, payload []byte) ([]byte, error) {
	params, ok := algorithmParams[alg]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedAlgorithm, alg)
	}
	if params.hash == 0 {
		return key.Sign(rand.Reader, payload, crypto.Hash(0))
	}
	h := params.hash.New()
	h.Write(payload)
	return key.Sign(rand.Reader, h.Sum(nil), params.hash)
}

// verifyWithCert verifies signature over payload with leaf's public key,
// using the Algorithm derived from that key. It returns
// ErrUnsupportedAlgorithm for an unsupported key and ErrSignatureMismatch
// when the signature does not verify.
func verifyWithCert(leaf *x509.Certificate, payload, signature []byte) error {
	alg, err := algorithmFromCert(leaf)
	if err != nil {
		return err
	}
	if err := leaf.CheckSignature(algorithmParams[alg].sigAlg, payload, signature); err != nil {
		return fmt.Errorf("%w: %v", ErrSignatureMismatch, err)
	}
	return nil
}
