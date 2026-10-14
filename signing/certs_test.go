package signing

import (
	"crypto/x509"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateCADefaults(t *testing.T) {
	before := time.Now()
	ca, key, err := GenerateCA(AlgorithmECDSAP256SHA256, CertOptions{})
	require.NoError(t, err)
	require.NotNil(t, key)

	assert.True(t, ca.IsCA)
	assert.True(t, ca.BasicConstraintsValid)
	assert.Equal(t, x509.KeyUsageCertSign|x509.KeyUsageDigitalSignature, ca.KeyUsage)
	assert.Equal(t, "opamp-go test CA (ECDSA-P256-SHA256)", ca.Subject.CommonName)
	assert.WithinDuration(t, before.Add(-time.Hour), ca.NotBefore, time.Minute)
	assert.WithinDuration(t, before.Add(24*time.Hour), ca.NotAfter, time.Minute)
	require.NoError(t, ca.CheckSignatureFrom(ca))
}

func TestGenerateLeafDefaultsAndOptions(t *testing.T) {
	ca, caKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	nb := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	na := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	ip := net.ParseIP("192.0.2.10")

	leaf, key, err := GenerateLeaf(AlgorithmECDSAP256SHA256, ca, caKey, CertOptions{
		NotBefore:   nb,
		NotAfter:    na,
		CommonName:  "custom-leaf",
		DNSNames:    []string{"a.example", "b.example"},
		IPAddresses: []net.IP{ip},
	})
	require.NoError(t, err)
	require.NotNil(t, key)

	assert.False(t, leaf.IsCA)
	assert.Equal(t, x509.KeyUsageDigitalSignature, leaf.KeyUsage)
	assert.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}, leaf.ExtKeyUsage)
	assert.Equal(t, "custom-leaf", leaf.Subject.CommonName)
	assert.Equal(t, []string{"a.example", "b.example"}, leaf.DNSNames)
	require.Len(t, leaf.IPAddresses, 1)
	assert.True(t, leaf.IPAddresses[0].Equal(ip))
	assert.True(t, leaf.NotBefore.Equal(nb))
	assert.True(t, leaf.NotAfter.Equal(na))
	require.NoError(t, leaf.CheckSignatureFrom(ca))

	leaf, _, err = GenerateLeaf(AlgorithmEd25519, ca, caKey, CertOptions{})
	require.NoError(t, err)
	assert.Equal(t, "opamp-go test leaf (Ed25519)", leaf.Subject.CommonName)
}

func TestGenerateCertAllAlgorithms(t *testing.T) {
	for _, alg := range allAlgorithms {
		t.Run(alg.String(), func(t *testing.T) {
			ca, caKey := newTestCA(t, alg)
			leaf, _ := newTestLeaf(t, alg, ca, caKey)
			got, err := algorithmFromCert(leaf)
			require.NoError(t, err)
			assert.Equal(t, alg, got)
			require.NoError(t, leaf.CheckSignatureFrom(ca))
		})
	}
}

// The certificate's signature algorithm follows the issuer's key, which may
// differ in type from the leaf's key.
func TestGenerateLeafCrossKeyType(t *testing.T) {
	cases := []struct{ ca, leaf Algorithm }{
		{AlgorithmRSAPKCS1v15SHA256, AlgorithmECDSAP256SHA256},
		{AlgorithmECDSAP384SHA384, AlgorithmEd25519},
		{AlgorithmEd25519, AlgorithmECDSAP384SHA384},
	}
	for _, c := range cases {
		t.Run(c.ca.String()+"->"+c.leaf.String(), func(t *testing.T) {
			ca, caKey := newTestCA(t, c.ca)
			leaf, _ := newTestLeaf(t, c.leaf, ca, caKey)
			require.NoError(t, leaf.CheckSignatureFrom(ca))
			_, err := ValidateChain(t.Context(), ders(leaf), rootPool(ca), time.Now(), testHost)
			require.NoError(t, err)
		})
	}
}

func TestGenerateUnsupportedAlgorithm(t *testing.T) {
	_, _, err := GenerateCA(AlgorithmUnspecified, CertOptions{})
	assert.ErrorIs(t, err, ErrUnsupportedAlgorithm)

	ca, caKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	_, _, err = GenerateLeaf(Algorithm(42), ca, caKey, CertOptions{})
	assert.ErrorIs(t, err, ErrUnsupportedAlgorithm)
}

func TestGenerateLeafWithoutIssuerKey(t *testing.T) {
	ca, _ := newTestCA(t, AlgorithmECDSAP256SHA256)
	_, _, err := GenerateLeaf(AlgorithmECDSAP256SHA256, ca, nil, CertOptions{})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "signing: create leaf cert"), err.Error())
}
