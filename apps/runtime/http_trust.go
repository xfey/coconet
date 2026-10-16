package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"
)

var customHTTPTrust struct {
	sync.Mutex
	digest    [32]byte
	transport *http.Transport
}

func coconetHTTPClient(timeout time.Duration) (*http.Client, error) {
	transport, err := coconetHTTPTransport()
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

// COCONET_CA_FILE adds an explicit private CA to system roots for Coconet's
// HTTPS clients only. Hostname, validity and Deployment pin checks stay intact.
// It never edits the OS trust store or disables certificate verification.
func coconetHTTPTransport() (http.RoundTripper, error) {
	path := os.Getenv("COCONET_CA_FILE")
	if path == "" {
		return nil, nil
	}
	data, err := readLocalPrivateFile(path, 1<<20, false)
	if err != nil {
		return nil, errors.New("COCONET_CA_FILE must be an absolute, readable regular PEM certificate file of at most 1 MiB")
	}
	digest := sha256.Sum256(data)
	customHTTPTrust.Lock()
	defer customHTTPTrust.Unlock()
	if customHTTPTrust.transport != nil && customHTTPTrust.digest == digest {
		return customHTTPTrust.transport, nil
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("could not load system TLS roots")
	}
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("COCONET_CA_FILE contains no valid PEM certificates")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("custom CA requires the standard HTTP transport")
	}
	transport := base.Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	if customHTTPTrust.transport != nil {
		customHTTPTrust.transport.CloseIdleConnections()
	}
	customHTTPTrust.digest, customHTTPTrust.transport = digest, transport
	return transport, nil
}
