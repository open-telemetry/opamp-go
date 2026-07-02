package signing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// CertOptions configures certificate generation in [GenerateCA] and
// [GenerateLeaf]. The zero value yields a 24-hour validity window
// starting one hour in the past (to absorb minor clock skew).
type CertOptions struct {
	// NotBefore overrides the validity start. Zero means
	// time.Now().Add(-1 * time.Hour).
	NotBefore time.Time
	// NotAfter overrides the validity end. Zero means
	// time.Now().Add(24 * time.Hour).
	NotAfter time.Time
	// CommonName overrides the certificate's Subject CommonName.
	CommonName string
	// DNSNames sets the dNSName Subject Alternative Name entries on the
	// leaf certificate. Per the OpAMP Message Attestation spec the leaf
	// MUST include a SAN that matches the OpAMP distribution server's
	// hostname so the Agent can bind the signing certificate to a
	// specific server during the connection-time handshake.
	DNSNames []string
	// IPAddresses sets the iPAddress Subject Alternative Name entries
	// on the leaf certificate. Use when the Agent connects to the
	// OpAMP server by IP address rather than hostname.
	IPAddresses []net.IP
}

func (o CertOptions) notBefore() time.Time {
	if !o.NotBefore.IsZero() {
		return o.NotBefore
	}
	return time.Now().Add(-1 * time.Hour)
}

func (o CertOptions) notAfter() time.Time {
	if !o.NotAfter.IsZero() {
		return o.NotAfter
	}
	return time.Now().Add(24 * time.Hour)
}

// GenerateCA produces a self-signed CA certificate and its
// corresponding private key for the supplied algorithm. The CA has
// KeyUsageCertSign + KeyUsageDigitalSignature and is marked CA:TRUE
// with a critical basicConstraints extension.
//
// Intended primarily for tests and for the opamp-go example server.
// Production deployments will use externally-managed CA infrastructure.
func GenerateCA(alg Algorithm, opts CertOptions) (*x509.Certificate, crypto.Signer, error) {
	key, sigAlg, pub, err := newKey(alg)
	if err != nil {
		return nil, nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	cn := opts.CommonName
	if cn == "" {
		cn = fmt.Sprintf("opamp-go test CA (%s)", alg)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             opts.notBefore(),
		NotAfter:              opts.notAfter(),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
		SignatureAlgorithm:    sigAlg,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, nil, fmt.Errorf("signing: create CA cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("signing: parse CA cert: %w", err)
	}
	return cert, key, nil
}

// GenerateLeaf produces a leaf signing certificate signed by ca with
// caKey, using alg. The leaf carries ExtKeyUsageCodeSigning (the EKU
// required by the OpAMP Message Attestation spec) and
// KeyUsageDigitalSignature.
//
// Intended primarily for tests and example servers.
func GenerateLeaf(alg Algorithm, ca *x509.Certificate, caKey crypto.Signer, opts CertOptions) (*x509.Certificate, crypto.Signer, error) {
	key, sigAlg, pub, err := newKey(alg)
	if err != nil {
		return nil, nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	cn := opts.CommonName
	if cn == "" {
		cn = fmt.Sprintf("opamp-go test leaf (%s)", alg)
	}

	tmpl := &x509.Certificate{
		SerialNumber:       serial,
		Subject:            pkix.Name{CommonName: cn},
		NotBefore:          opts.notBefore(),
		NotAfter:           opts.notAfter(),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		SignatureAlgorithm: sigAlg,
		DNSNames:           opts.DNSNames,
		IPAddresses:        opts.IPAddresses,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("signing: create leaf cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("signing: parse leaf cert: %w", err)
	}
	return cert, key, nil
}

// newKey creates a private key for alg and returns the corresponding
// x509.SignatureAlgorithm to record in certificates, along with the
// public-key form needed by x509.CreateCertificate.
func newKey(alg Algorithm) (crypto.Signer, x509.SignatureAlgorithm, crypto.PublicKey, error) {
	switch alg {
	case AlgorithmECDSAP256SHA256:
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("signing: generate ECDSA-P256 key: %w", err)
		}
		return k, x509.ECDSAWithSHA256, &k.PublicKey, nil
	case AlgorithmECDSAP384SHA384:
		k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("signing: generate ECDSA-P384 key: %w", err)
		}
		return k, x509.ECDSAWithSHA384, &k.PublicKey, nil
	case AlgorithmRSAPKCS1v15SHA256:
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("signing: generate RSA-2048 key: %w", err)
		}
		return k, x509.SHA256WithRSA, &k.PublicKey, nil
	case AlgorithmEd25519:
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("signing: generate Ed25519 key: %w", err)
		}
		return priv, x509.PureEd25519, pub, nil
	default:
		return nil, 0, nil, fmt.Errorf("%w: %d", ErrUnsupportedAlgorithm, alg)
	}
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("signing: generate serial: %w", err)
	}
	return n, nil
}
