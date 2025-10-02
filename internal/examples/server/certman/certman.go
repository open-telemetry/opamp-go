package certman

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/internal/examples/certs"
	"github.com/open-telemetry/opamp-go/protobufs"
)

var logger = log.New(log.Default().Writer(), "[CertMan] ", log.Default().Flags()|log.Lmsgprefix|log.Lmicroseconds)

var (
	caCert      *x509.Certificate
	caPrivKey   *rsa.PrivateKey
	caCertBytes []byte
)

var loadCACertOnce sync.Once

func loadCACert() {
	// Convert from DER to PEM format.
	caCertPB, _ := pem.Decode(certs.CaCert)
	caKeyPB, _ := pem.Decode(certs.CaKey)

	var err error

	caCert, err = x509.ParseCertificate(caCertPB.Bytes)
	if err != nil {
		logger.Fatalf("Cannot parse CA certificate: %v", err)
	}

	caPrivKey, err = x509.ParsePKCS1PrivateKey(caKeyPB.Bytes)
	if err != nil {
		logger.Fatalf("Cannot parse CA key: %v", err)
	}
}

func createClientTLSCertTemplate() *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(1),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour * 1000),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
}

func CreateClientTLSCertFromCSR(csr *x509.CertificateRequest) (*protobufs.TLSCertificate, error) {
	loadCACertOnce.Do(loadCACert)

	template := createClientTLSCertTemplate()

	// Use the Subject from CSR.
	template.Subject = csr.Subject

	// Create the client cert and sign it using CA cert.
	certBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, csr.PublicKey, caPrivKey)
	if err != nil {
		err := fmt.Errorf("cannot create certificate: %v", err)
		return nil, err
	}

	// Convert from DER to PEM format.
	certPEM := new(bytes.Buffer)
	pem.Encode(
		certPEM, &pem.Block{
			Type:  "CERTIFICATE",
			Bytes: certBytes,
		},
	)

	// We have a client certificate with a public and private key.
	certificate := &protobufs.TLSCertificate{
		Cert:   certPEM.Bytes(),
		CaCert: caCertBytes,
	}

	return certificate, nil
}

func CreateClientTLSCert() (*protobufs.TLSCertificate, error) {
	loadCACertOnce.Do(loadCACert)

	// Generate a keypair for new client cert.
	clientCertKeyPair, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		err := fmt.Errorf("cannot generate keypair: %v", err)
		return nil, err
	}

	// Prepare certificate template.
	template := createClientTLSCertTemplate()
	template.Subject = pkix.Name{
		CommonName:   "OpAMP Example Client",
		Organization: []string{"OpenTelemetry OpAMP Workgroup"},
		Locality:     []string{"Server-initiated"},
	}

	// Create the client cert. Sign it using CA cert.
	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &clientCertKeyPair.PublicKey, caPrivKey)
	if err != nil {
		err := fmt.Errorf("cannot create certificate: %v", err)
		return nil, err
	}

	certPEM := new(bytes.Buffer)
	pem.Encode(
		certPEM, &pem.Block{
			Type:  "CERTIFICATE",
			Bytes: certDER,
		},
	)

	privateKeyPEM := new(bytes.Buffer)
	pem.Encode(
		privateKeyPEM, &pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(clientCertKeyPair),
		},
	)

	// We have a client certificate with a public and private key.
	certificate := &protobufs.TLSCertificate{
		Cert:       certPEM.Bytes(),
		PrivateKey: privateKeyPEM.Bytes(),
		CaCert:     caCertBytes,
	}

	return certificate, nil
}
