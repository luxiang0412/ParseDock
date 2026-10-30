// Package tlsutil builds the mutual-TLS configuration used between the API
// replicas, the Coordinator and the workers.
//
// The Phase 0 design used a shared internal token over a private network. That
// token remains as an application-level authorisation layer; mTLS adds transport
// authentication so a compromised host on the private network cannot impersonate a
// worker or an API replica.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"
)

// ServerConfig returns a TLS config for the Coordinator's internal listener.
//
//   - certFile/keyFile are required.
//   - clientCAFile, when set, requires and verifies a client certificate (mTLS).
func ServerConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("tls: server certificate and key are required")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("tls: load server keypair: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if clientCAFile != "" {
		pool, err := loadPool(clientCAFile)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// ClientConfig returns a TLS config for API replicas and workers.
//
//   - caFile, when set, verifies the server certificate.
//   - certFile/keyFile, when set, present a client certificate (mTLS).
//   - serverName overrides the name verified against the certificate; required
//     when dialling an IP address whose SAN does not include it.
func ClientConfig(caFile, certFile, keyFile, serverName string, insecure bool) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName, InsecureSkipVerify: insecure}
	if caFile != "" {
		pool, err := loadPool(caFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if certFile != "" || keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("tls: load client keypair: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

func loadPool(path string) (*x509.CertPool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tls: read CA %s: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("tls: %s contains no PEM certificates", path)
	}
	return pool, nil
}

// Transport returns a client transport that keeps the standard dial and handshake
// timeouts (a zero-valued Transport has none, so a stalled handshake would block a
// worker's registration forever) and applies cfg. When cfg is nil the default
// transport is cloned unchanged, so callers can always use the result.
func Transport(cfg *tls.Config) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	t := base.Clone()
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 32
	t.IdleConnTimeout = 90 * time.Second
	if cfg != nil {
		t.TLSClientConfig = cfg
	}
	return t
}
