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
	// DNSNames sets the leaf's dNSName SANs. The leaf MUST cover the host
	// Agents connect to; list every such host (for example a gateway and
	// the origin server) to accept Agents connecting through any of them.
	DNSNames []string
	// IPAddresses sets the leaf's iPAddress SANs, for Agents that connect
	// by IP address.
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

// GenerateCA produces a self-signed CA certificate and key for alg, for
// tests and examples.
func GenerateCA(alg Algorithm, opts CertOptions) (*x509.Certificate, crypto.Signer, error) {
	tmpl := &x509.Certificate{
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	return generateCert(alg, opts, "CA", tmpl, nil, nil)
}

// GenerateLeaf produces a signing leaf and key for alg, issued by ca, for
// tests and examples. The leaf carries the code-signing EKU the spec requires.
func GenerateLeaf(alg Algorithm, ca *x509.Certificate, caKey crypto.Signer, opts CertOptions) (*x509.Certificate, crypto.Signer, error) {
	tmpl := &x509.Certificate{
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		DNSNames:    opts.DNSNames,
		IPAddresses: opts.IPAddresses,
	}
	return generateCert(alg, opts, "leaf", tmpl, ca, caKey)
}

// generateCert issues tmpl with a fresh alg key; a nil parent means
// self-signed. SignatureAlgorithm is left unset so crypto/x509 derives it
// from the issuer's key, which may differ in type from the new key.
func generateCert(alg Algorithm, opts CertOptions, kind string, tmpl, parent *x509.Certificate, parentKey crypto.Signer) (*x509.Certificate, crypto.Signer, error) {
	key, err := newKey(alg)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	cn := opts.CommonName
	if cn == "" {
		cn = fmt.Sprintf("opamp-go test %s (%s)", kind, alg)
	}
	tmpl.SerialNumber = serial
	tmpl.Subject = pkix.Name{CommonName: cn}
	tmpl.NotBefore = opts.notBefore()
	tmpl.NotAfter = opts.notAfter()
	if parent == nil {
		parent, parentKey = tmpl, key
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, key.Public(), parentKey)
	if err != nil {
		return nil, nil, fmt.Errorf("signing: create %s cert: %w", kind, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("signing: parse %s cert: %w", kind, err)
	}
	return cert, key, nil
}

// newKey creates a private key for alg.
func newKey(alg Algorithm) (crypto.Signer, error) {
	var (
		key crypto.Signer
		err error
	)
	switch alg {
	case AlgorithmECDSAP256SHA256:
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case AlgorithmECDSAP384SHA384:
		key, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case AlgorithmRSAPKCS1v15SHA256:
		key, err = rsa.GenerateKey(rand.Reader, rsaMinModulusBits)
	case AlgorithmEd25519:
		_, key, err = ed25519.GenerateKey(rand.Reader)
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedAlgorithm, alg)
	}
	if err != nil {
		return nil, fmt.Errorf("signing: generate %s key: %w", alg, err)
	}
	return key, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("signing: generate serial: %w", err)
	}
	return n, nil
}
