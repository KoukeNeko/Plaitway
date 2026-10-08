//go:build unix

package ovpn

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// pki is a throw-away certificate authority with one server and one client
// certificate: RSA 2048, SHA-256, valid for a day. It exists only inside a
// test; nothing here resembles a real credential.
type pki struct {
	CA, ServerCert, ServerKey, ClientCert, ClientKey string
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	caKey := newRSAKey(t)
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Plaitway Test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SignatureAlgorithm:    x509.SHA256WithRSA,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	issue := func(serial int64, cn string, usage x509.ExtKeyUsage, ips []net.IP) (certPEM, keyPEM string) {
		key := newRSAKey(t)
		template := &x509.Certificate{
			SerialNumber:       big.NewInt(serial),
			Subject:            pkix.Name{CommonName: cn},
			NotBefore:          now.Add(-time.Hour),
			NotAfter:           now.Add(24 * time.Hour),
			KeyUsage:           x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:        []x509.ExtKeyUsage{usage},
			IPAddresses:        ips,
			SignatureAlgorithm: x509.SHA256WithRSA,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
			string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	}

	p := &pki{CA: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))}
	p.ServerCert, p.ServerKey = issue(2, "plaitway-test-server", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	p.ClientCert, p.ClientKey = issue(3, "plaitway-test-client", x509.ExtKeyUsageClientAuth, nil)
	return p
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
