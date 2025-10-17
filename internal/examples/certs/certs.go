package certs

// This file contains helper functions to
// create TLS configs/certificates. Currently used in
// the example client and server and
// in the tests. Not intended for any other use.

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
)

//go:embed certs/ca.cert.pem
var CaCert []byte

//go:embed private/ca.key.pem
var CaKey []byte

//go:embed server_certs/server.cert.pem
var ServerCert []byte

//go:embed server_certs/server.key.pem
var ServerKey []byte

func CreateClientTLSConfig(clientCert *tls.Certificate, caCertBytes []byte) (*tls.Config, error) {
	// Create a certificate pool and make our CA trusted.
	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(caCertBytes); !ok {
		return nil, errors.New("cannot append ca.cert.pem")
	}

	cfg := &tls.Config{
		RootCAs: caCertPool,
	}
	if clientCert != nil {
		// If there is a client-side certificate use it for connection too.
		cfg.Certificates = []tls.Certificate{*clientCert}
	}
	return cfg, nil
}

func CreateServerTLSConfig(caCertBytes, serverCertBytes, serverKeyBytes []byte) (*tls.Config, error) {
	// Create a certificate pool and make our CA trusted.
	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(caCertBytes); !ok {
		return nil, errors.New("cannot append ca.cert.pem")
	}

	cert, err := tls.X509KeyPair(serverCertBytes, serverKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("tls.X509KeyPair failed: %v", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// TODO: verify client cert manually, and allow TOFU option. See manual
		// verification example: https://dev.to/living_syn/validating-client-certificate-sans-in-go-i5p
		// Instead, we use VerifyClientCertIfGiven which will automatically verify the provided certificate
		// is signed by our CA (so TOFU with self-generated client certificate will not work).
		ClientAuth: tls.VerifyClientCertIfGiven,
		// Allow insecure connections for demo purposes.
		InsecureSkipVerify: true,
		ClientCAs:          caCertPool,
	}
	return tlsConfig, nil
}

func CreateServerTLSConfigFromFiles(caCertPath, serverCertPath, serverKeyPath string) (*tls.Config, error) {
	// Read the CA's public key. This is the CA that signs the server's certificate.
	caCertBytes, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, err
	}

	// Create a certificate pool and make our CA trusted.
	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(caCertBytes); !ok {
		return nil, errors.New("cannot append ca.cert.pem")
	}

	// Load server's certificate.
	cert, err := tls.LoadX509KeyPair(
		serverCertPath,
		serverKeyPath,
	)
	if err != nil {
		return nil, fmt.Errorf("tls.LoadX509KeyPair failed: %v", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// TODO: verify client cert manually, and allow TOFU option. See manual
		// verification example: https://dev.to/living_syn/validating-client-certificate-sans-in-go-i5p
		// Instead, we use VerifyClientCertIfGiven which will automatically verify the provided certificate
		// is signed by our CA (so TOFU with self-generated client certificate will not work).
		ClientAuth: tls.VerifyClientCertIfGiven,
		// Allow insecure connections for demo purposes.
		InsecureSkipVerify: true,
		ClientCAs:          caCertPool,
	}
	return tlsConfig, nil
}

func CreateTLSCert(caCertBytes, caKeyBytes []byte) (*protobufs.TLSCertificate, error) {
	caCertPB, _ := pem.Decode(caCertBytes)
	caKeyPB, _ := pem.Decode(caKeyBytes)
	caCert, err := x509.ParseCertificate(caCertPB.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cannot parse CA cert: %v", err)
	}

	caPrivKey, err := x509.ParsePKCS1PrivateKey(caKeyPB.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cannot parse CA key: %v", err)
	}

	// Generate a private key for new client cert.
	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		err := fmt.Errorf("cannot generate private key: %v", err)
		return nil, err
	}

	// Prepare certificate template.
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:    "OpAMP Example Client",
			Organization:  []string{"OpAMP Example"},
			Country:       []string{"CA"},
			Province:      []string{"ON"},
			Locality:      []string{"City"},
			StreetAddress: []string{""},
			PostalCode:    []string{""},
		},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(time.Hour * 1000),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature,
	}

	// Create the client cert. Sign it using CA cert.
	certBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &certPrivKey.PublicKey, caPrivKey)
	if err != nil {
		err := fmt.Errorf("cannot create certificate: %v", err)
		return nil, err
	}

	publicKeyPEM := new(bytes.Buffer)
	pem.Encode(publicKeyPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})

	privateKeyPEM := new(bytes.Buffer)
	pem.Encode(privateKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey),
	})

	// We have a client certificate with a public and private key.
	certificate := &protobufs.TLSCertificate{
		Cert:       publicKeyPEM.Bytes(),
		PrivateKey: privateKeyPEM.Bytes(),
		CaCert:     caCertBytes,
	}

	return certificate, nil
}
