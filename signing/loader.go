package signing

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// ErrLoadCAFile wraps failures to read or parse the operator-supplied
// trust anchor PEM file.
var ErrLoadCAFile = errors.New("signing: load CA file")

// ErrParsePrivateKey wraps failures to decode a PEM-encoded private key.
var ErrParsePrivateKey = errors.New("signing: parse private key")

// VerifierFromFile constructs a LocalVerifier trusting the PEM certificates
// in the file at caPath. Non-CERTIFICATE blocks are ignored.
func VerifierFromFile(caPath string) (*LocalVerifier, error) {
	if caPath == "" {
		return nil, fmt.Errorf("%w: empty path", ErrLoadCAFile)
	}
	data, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoadCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("%w: no valid PEM certificates in %s", ErrLoadCAFile, caPath)
	}
	return NewLocalVerifier(pool)
}

// VerifierFromPEM constructs a LocalVerifier trusting the PEM certificates
// in pemBytes.
func VerifierFromPEM(pemBytes []byte) (*LocalVerifier, error) {
	if len(pemBytes) == 0 {
		return nil, fmt.Errorf("%w: empty PEM bytes", ErrLoadCAFile)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("%w: no valid PEM certificates in supplied bytes", ErrLoadCAFile)
	}
	return NewLocalVerifier(pool)
}

// LocalSignerFromFiles constructs a LocalSigner from a PEM private key
// (PKCS#8, EC, or PKCS#1) at keyPath and a PEM chain (intermediates first,
// leaf last, root excluded) at chainPath.
func LocalSignerFromFiles(keyPath, chainPath string) (*LocalSigner, error) {
	if keyPath == "" {
		return nil, errors.New("signing: empty key path")
	}
	if chainPath == "" {
		return nil, errors.New("signing: empty chain path")
	}

	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("signing: read key: %w", err)
	}
	chainBytes, err := os.ReadFile(chainPath)
	if err != nil {
		return nil, fmt.Errorf("signing: read chain: %w", err)
	}

	key, err := parsePrivateKeyPEM(keyBytes)
	if err != nil {
		return nil, err
	}

	chain, err := parseCertChainPEM(chainBytes)
	if err != nil {
		return nil, err
	}
	return NewLocalSigner(key, chain)
}

func parsePrivateKeyPEM(data []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrParsePrivateKey)
	}
	// PKCS#8 covers RSA, ECDSA, and Ed25519; PKCS#1 and EC are legacy forms.
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		s, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("%w: PKCS#8 key type %T does not implement crypto.Signer", ErrParsePrivateKey, k)
		}
		return s, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("%w: tried PKCS#8, PKCS#1, EC — none matched", ErrParsePrivateKey)
}

func parseCertChainPEM(data []byte) ([]*x509.Certificate, error) {
	var chain []*x509.Certificate
	for _, der := range pemCertificates(data) {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("signing: parse certificate in chain: %w", err)
		}
		chain = append(chain, cert)
	}
	return chain, nil
}

// pemCertificates returns the DER bytes of every CERTIFICATE block in data,
// in order. Other PEM block types are skipped.
func pemCertificates(data []byte) [][]byte {
	var ders [][]byte
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return ders
		}
		if block.Type == "CERTIFICATE" {
			ders = append(ders, block.Bytes)
		}
	}
}
