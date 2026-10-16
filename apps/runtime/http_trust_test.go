package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateCATrustIsExplicitAndKeepsHostnameVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	t.Setenv("COCONET_CA_FILE", "")
	client, _ := newAPIClient(server.URL, "")
	if response, err := client.httpClient.Get(server.URL); err == nil {
		response.Body.Close()
		t.Fatal("system roots unexpectedly accepted isolated test certificate")
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COCONET_CA_FILE", file)
	client, err := newAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.httpClient.Get(server.URL)
	if err != nil {
		t.Fatal("explicit private CA was not used", err)
	}
	response.Body.Close()
	// A trusted key still cannot authorize an unrelated TLS hostname.
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unrelated.invalid"}, DNSNames: []string{"unrelated.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	other.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	other.StartTLS()
	defer other.Close()
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	client, err = newAPIClient(other.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.httpClient.Get(other.URL); err == nil {
		response.Body.Close()
		t.Fatal("private CA disabled hostname checking")
	}
	if err := os.WriteFile(file, []byte("invalid certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newAPIClient(server.URL, ""); err == nil {
		t.Fatal("invalid changed CA silently reused old trust")
	}
	t.Setenv("COCONET_CA_FILE", "relative.pem")
	if _, err := newAPIClient(server.URL, ""); err == nil {
		t.Fatal("relative CA path accepted")
	}
}
