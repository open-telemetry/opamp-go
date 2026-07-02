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

// ErrParsePrivateKey wraps failures to decode a PEM-encoded private
// key. Multiple PKCS encodings are tried in turn (PKCS#8, EC, PKCS#1).
var ErrParsePrivateKey = errors.New("signing: parse private key")

// VerifierFromFile constructs a LocalVerifier whose trust anchor pool
// is populated from a PEM file at caPath. The file MUST contain one or
// more PEM-encoded X.509 certificates; any non-CERTIFICATE PEM blocks
// (for example RSA PRIVATE KEY blocks accidentally left in the file)
// are ignored.
//
// Typical use: the opamp-go client supervisor or extension calls this
// at startup with the operator-supplied payload_ca path.
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

// VerifierFromPEM constructs a LocalVerifier whose trust anchor pool is
// populated from pemBytes. Useful when the CA certificate bytes are already
// in memory (for example, after a TOFU enrollment).
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

// LocalSignerFromFiles constructs a LocalSigner from PEM-encoded files:
//
//   - keyPath:   path to a PEM file containing the leaf signing private
//     key. PKCS#8, EC, and PKCS#1 encodings are accepted.
//   - chainPath: path to a PEM file containing the certificate chain.
//     The chain MUST be ordered intermediates first, leaf last, and the
//     leaf cert MUST correspond to the private key. The root MUST NOT
//     be included.
//
// Intended for example servers, smoke tests, and any deployment that
// stores signing material as PEM files on disk.
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
	if len(chain) == 0 {
		return nil, ErrEmptyChain
	}

	return NewLocalSigner(key, chain)
}

func parsePrivateKeyPEM(data []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrParsePrivateKey)
	}
	// PKCS#8 first — covers RSA, ECDSA, and Ed25519 in one call. If it
	// succeeds, we accept any key type that implements crypto.Signer
	// (which all current and likely-future stdlib private-key types
	// do).
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		s, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("%w: PKCS#8 key type %T does not implement crypto.Signer", ErrParsePrivateKey, k)
		}
		return s, nil
	}
	// PKCS#1 for legacy RSA private keys.
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	// EC for legacy ECDSA private keys.
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("%w: tried PKCS#8, PKCS#1, EC — none matched", ErrParsePrivateKey)
}

func parseCertChainPEM(data []byte) ([]*x509.Certificate, error) {
	var chain []*x509.Certificate
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("signing: parse certificate in chain: %w", err)
		}
		chain = append(chain, cert)
	}
	return chain, nil
}
