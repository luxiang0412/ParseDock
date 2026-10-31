package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pki writes a throwaway CA, a server certificate for 127.0.0.1 and a client
// certificate, all signed by the CA, and returns their paths.
type pki struct {
	caCert, serverCert, serverKey, clientCert, clientKey string
}

func newPKI(t *testing.T) pki {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	p := pki{caCert: filepath.Join(dir, "ca.crt")}
	writePEM(t, p.caCert, "CERTIFICATE", caDER)

	issue := func(name string, serial int64, server bool) (string, string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
		}
		if server {
			tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		} else {
			tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		kb, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		c, k := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
		writePEM(t, c, "CERTIFICATE", der)
		writePEM(t, k, "EC PRIVATE KEY", kb)
		return c, k
	}
	p.serverCert, p.serverKey = issue("server", 2, true)
	p.clientCert, p.clientKey = issue("client", 3, false)
	return p
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestServerConfigRequiresCertAndKey(t *testing.T) {
	if _, err := ServerConfig("", "", ""); err == nil {
		t.Fatal("ServerConfig without cert/key must fail")
	}
	p := newPKI(t)
	if _, err := ServerConfig(p.serverCert, "", p.caCert); err == nil {
		t.Fatal("a client CA without a server key must fail (no silent plaintext)")
	}
}

func TestServerConfigMutualTLS(t *testing.T) {
	p := newPKI(t)
	plain, err := ServerConfig(p.serverCert, p.serverKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if plain.ClientAuth != tls.NoClientCert || plain.MinVersion != tls.VersionTLS12 {
		t.Fatalf("server-only TLS: ClientAuth=%v MinVersion=%x", plain.ClientAuth, plain.MinVersion)
	}
	mutual, err := ServerConfig(p.serverCert, p.serverKey, p.caCert)
	if err != nil {
		t.Fatal(err)
	}
	if mutual.ClientAuth != tls.RequireAndVerifyClientCert || mutual.ClientCAs == nil {
		t.Fatalf("mTLS must require and verify client certificates, got %v", mutual.ClientAuth)
	}
}

func TestLoadPoolRejectsGarbage(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ClientConfig(bad, "", "", "", false); err == nil || !strings.Contains(err.Error(), "no PEM") {
		t.Fatalf("garbage CA must be rejected, got %v", err)
	}
	if _, err := ClientConfig(filepath.Join(t.TempDir(), "missing.pem"), "", "", "", false); err == nil {
		t.Fatal("missing CA file must be rejected")
	}
}

func TestClientConfigHalfKeypairFails(t *testing.T) {
	p := newPKI(t)
	if _, err := ClientConfig(p.caCert, p.clientCert, "", "", false); err == nil {
		t.Fatal("a client certificate without its key must fail")
	}
}

// TestMutualTLSRoundTrip proves the three helpers compose: a client with the CA and a
// client certificate gets through, one without a certificate is refused.
func TestMutualTLSRoundTrip(t *testing.T) {
	p := newPKI(t)
	serverTLS, err := ServerConfig(p.serverCert, p.serverKey, p.caCert)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.TLS = serverTLS
	srv.StartTLS()
	defer srv.Close()

	good, err := ClientConfig(p.caCert, p.clientCert, p.clientKey, "", false)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: Transport(good)}).Get(srv.URL)
	if err != nil {
		t.Fatalf("mTLS client refused: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "client" {
		t.Fatalf("peer CN = %q, want client", body)
	}

	noCert, err := ClientConfig(p.caCert, "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := (&http.Client{Transport: Transport(noCert)}).Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a client without a certificate must be refused by an mTLS listener")
	}
}

func TestTransportTimeouts(t *testing.T) {
	tr := Transport(nil)
	if tr.ResponseHeaderTimeout != WorkerResponseHeaderTimeout {
		t.Fatalf("worker transport header timeout = %s, want %s", tr.ResponseHeaderTimeout, WorkerResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Fatal("the cloned default transport must keep a TLS handshake timeout")
	}
	if tr.TLSClientConfig != nil && len(tr.TLSClientConfig.Certificates) > 0 {
		t.Fatal("nil cfg must not invent a client certificate")
	}
	long := TransportWithResponseHeaderTimeout(&tls.Config{ServerName: "x"}, 150*time.Second)
	if long.ResponseHeaderTimeout != 150*time.Second || long.TLSClientConfig.ServerName != "x" {
		t.Fatalf("explicit timeout/config not applied: %s %+v", long.ResponseHeaderTimeout, long.TLSClientConfig)
	}
}

// TestResponseHeaderTimeoutIsEnforced checks the knob actually bounds a slow upstream
// (with a short value, so the test stays fast).
func TestResponseHeaderTimeoutIsEnforced(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	c := &http.Client{Transport: TransportWithResponseHeaderTimeout(nil, 50*time.Millisecond)}
	resp, err := c.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a stalled upstream must hit the response-header timeout")
	}
}
