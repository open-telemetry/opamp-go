package signing

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testHost = "opamp.example.com"

var allAlgorithms = []Algorithm{
	AlgorithmECDSAP256SHA256,
	AlgorithmECDSAP384SHA384,
	AlgorithmRSAPKCS1v15SHA256,
	AlgorithmEd25519,
}

func newTestCA(t *testing.T, alg Algorithm) (*x509.Certificate, crypto.Signer) {
	t.Helper()
	ca, key, err := GenerateCA(alg, CertOptions{})
	require.NoError(t, err)
	return ca, key
}

func newTestLeaf(t *testing.T, alg Algorithm, ca *x509.Certificate, caKey crypto.Signer, hosts ...string) (*x509.Certificate, crypto.Signer) {
	t.Helper()
	if len(hosts) == 0 {
		hosts = []string{testHost}
	}
	leaf, key, err := GenerateLeaf(alg, ca, caKey, CertOptions{DNSNames: hosts})
	require.NoError(t, err)
	return leaf, key
}

func rootPool(certs ...*x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range certs {
		p.AddCert(c)
	}
	return p
}

// issueCert creates a certificate from tmpl with sensible defaults filled in.
// A nil parent makes it self-signed with signerKey.
func issueCert(t *testing.T, tmpl, parent *x509.Certificate, pub crypto.PublicKey, signerKey crypto.Signer) *x509.Certificate {
	t.Helper()
	if tmpl.SerialNumber == nil {
		tmpl.SerialNumber = big.NewInt(time.Now().UnixNano())
	}
	if tmpl.Subject.CommonName == "" {
		tmpl.Subject = pkix.Name{CommonName: "test"}
	}
	if tmpl.NotBefore.IsZero() {
		tmpl.NotBefore = time.Now().Add(-time.Hour)
	}
	if tmpl.NotAfter.IsZero() {
		tmpl.NotAfter = time.Now().Add(time.Hour)
	}
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signerKey)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

func pemCert(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func ders(certs ...*x509.Certificate) [][]byte {
	out := make([][]byte, len(certs))
	for i, c := range certs {
		out[i] = c.Raw
	}
	return out
}

// opaqueSigner hides the concrete key type, like an HSM- or KMS-backed signer.
type opaqueSigner struct{ inner crypto.Signer }

func (o opaqueSigner) Public() crypto.PublicKey { return o.inner.Public() }
func (o opaqueSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return o.inner.Sign(r, digest, opts)
}
