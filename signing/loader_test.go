package signing

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, data, 0o600))
	return p
}

func pemBlock(typ string, b []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b})
}

func TestVerifierFromFile(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, _ := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)

	_, err := VerifierFromFile("")
	assert.ErrorIs(t, err, ErrLoadCAFile)

	_, err = VerifierFromFile(filepath.Join(t.TempDir(), "missing.pem"))
	assert.ErrorIs(t, err, ErrLoadCAFile)

	_, err = VerifierFromFile(writeFile(t, "empty.pem", pemBlock("PRIVATE KEY", []byte("x"))))
	assert.ErrorIs(t, err, ErrLoadCAFile)

	// Non-CERTIFICATE blocks alongside the CA are ignored.
	data := append(pemBlock("RSA PRIVATE KEY", []byte("stray")), pemCert(root)...)
	v, err := VerifierFromFile(writeFile(t, "ca.pem", data))
	require.NoError(t, err)
	_, err = v.ValidateChain(t.Context(), ders(leaf), time.Now(), testHost)
	assert.NoError(t, err)
}

func TestVerifierFromPEM(t *testing.T) {
	root, _ := newTestCA(t, AlgorithmECDSAP256SHA256)

	_, err := VerifierFromPEM(nil)
	assert.ErrorIs(t, err, ErrLoadCAFile)

	_, err = VerifierFromPEM([]byte("garbage"))
	assert.ErrorIs(t, err, ErrLoadCAFile)

	v, err := VerifierFromPEM(pemCert(root))
	require.NoError(t, err)
	assert.NotNil(t, v)
}

func TestLocalSignerFromFilesKeyFormats(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	ecLeaf, ecKey := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)
	rsaLeaf, rsaKey := newTestLeaf(t, AlgorithmRSAPKCS1v15SHA256, root, rootKey)
	edLeaf, edKey := newTestLeaf(t, AlgorithmEd25519, root, rootKey)

	pkcs8 := func(k crypto.Signer) []byte {
		der, err := x509.MarshalPKCS8PrivateKey(k)
		require.NoError(t, err)
		return pemBlock("PRIVATE KEY", der)
	}
	sec1, err := x509.MarshalECPrivateKey(ecKey.(*ecdsa.PrivateKey))
	require.NoError(t, err)

	cases := []struct {
		name string
		key  []byte
		leaf *x509.Certificate
		alg  Algorithm
	}{
		{"pkcs8 ecdsa", pkcs8(ecKey), ecLeaf, AlgorithmECDSAP256SHA256},
		{"pkcs8 rsa", pkcs8(rsaKey), rsaLeaf, AlgorithmRSAPKCS1v15SHA256},
		{"pkcs8 ed25519", pkcs8(edKey), edLeaf, AlgorithmEd25519},
		{"pkcs1 rsa", pemBlock("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey.(*rsa.PrivateKey))), rsaLeaf, AlgorithmRSAPKCS1v15SHA256},
		{"sec1 ecdsa", pemBlock("EC PRIVATE KEY", sec1), ecLeaf, AlgorithmECDSAP256SHA256},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := LocalSignerFromFiles(writeFile(t, "key.pem", c.key), writeFile(t, "chain.pem", pemCert(c.leaf)))
			require.NoError(t, err)
			assert.Equal(t, c.alg, s.Algorithm())
		})
	}
}

func TestLocalSignerFromFilesErrors(t *testing.T) {
	root, rootKey := newTestCA(t, AlgorithmECDSAP256SHA256)
	leaf, key := newTestLeaf(t, AlgorithmECDSAP256SHA256, root, rootKey)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	goodKey := writeFile(t, "key.pem", pemBlock("PRIVATE KEY", keyDER))
	goodChain := writeFile(t, "chain.pem", pemCert(leaf))
	missing := filepath.Join(t.TempDir(), "missing.pem")

	x25519, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	x25519DER, err := x509.MarshalPKCS8PrivateKey(x25519)
	require.NoError(t, err)

	_, err = LocalSignerFromFiles("", goodChain)
	assert.Error(t, err)
	_, err = LocalSignerFromFiles(goodKey, "")
	assert.Error(t, err)
	_, err = LocalSignerFromFiles(missing, goodChain)
	assert.Error(t, err)
	_, err = LocalSignerFromFiles(goodKey, missing)
	assert.Error(t, err)

	for name, keyPEM := range map[string][]byte{
		"no pem block": []byte("not pem"),
		"unparseable":  pemBlock("PRIVATE KEY", []byte("garbage")),
		"not a signer": pemBlock("PRIVATE KEY", x25519DER),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LocalSignerFromFiles(writeFile(t, "key.pem", keyPEM), goodChain)
			assert.ErrorIs(t, err, ErrParsePrivateKey)
		})
	}

	_, err = LocalSignerFromFiles(goodKey, writeFile(t, "bad.pem", pemBlock("CERTIFICATE", []byte("garbage"))))
	assert.Error(t, err)

	_, err = LocalSignerFromFiles(goodKey, writeFile(t, "nocerts.pem", pemBlock("PRIVATE KEY", keyDER)))
	assert.ErrorIs(t, err, ErrEmptyChain)
}
