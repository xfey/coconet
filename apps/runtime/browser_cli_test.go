package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"slices"
	"testing"
)

// The browser harness invokes the normal CLI entry point in an isolated test
// process. Trust only that fixture's certificate, without changing OS trust or
// adding a certificate override to the shipped CLI (macOS ignores SSL_CERT_FILE).
func TestBrowserCLIProcess(t *testing.T) {
	certificate := os.Getenv("COCONET_BROWSER_FIXTURE_CA")
	if certificate == "" {
		t.Skip("isolated browser CLI process is opt-in")
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		t.Fatal("missing isolated CLI arguments")
	}
	pem, err := os.ReadFile(certificate)
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(pem) {
		t.Fatal("invalid fixture certificate")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	os.Exit(run(context.Background(), os.Args[separator+1:], os.Stdin, os.Stdout, os.Stderr))
}
