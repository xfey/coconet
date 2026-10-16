package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func TestOIDCVerifiesIssuerAudienceSignatureExpiryAndNonce(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "fixture", Algorithm: string(jose.ES256), Use: "sig"}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jwk}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	var invalidSignature bool
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/keys", "id_token_signing_alg_values_supported": []string{"ES256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
		case "/token":
			payload, _ := json.Marshal(claims)
			signed, signErr := signer.Sign(payload)
			if signErr != nil {
				http.Error(w, "sign failed", 500)
				return
			}
			encoded, _ := signed.CompactSerialize()
			if invalidSignature {
				encoded = encoded[:strings.LastIndexByte(encoded, '.')+1] + base64.RawURLEncoding.EncodeToString(make([]byte, 64))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "fixture-access", "token_type": "bearer", "id_token": encoded})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, server.Client())
	discovered, err := oidc.NewProvider(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	provider := &loginProvider{Kind: "oidc", Issuer: server.URL, HTTP: server.Client(), OAuth: oauth2.Config{ClientID: "coconet-fixture", ClientSecret: "fixture-secret", Endpoint: discovered.Endpoint()}, Verifier: discovered.Verifier(&oidc.Config{ClientID: "coconet-fixture"})}
	for _, test := range []struct {
		name, field string
		value       any
		accept      bool
	}{
		{"valid", "", nil, true},
		{"wrong issuer", "iss", "https://other.test", false},
		{"wrong audience", "aud", "another-app", false},
		{"expired", "exp", time.Now().Add(-time.Hour).Unix(), false},
		{"wrong nonce", "nonce", "other-request", false},
		{"invalid signature", "signature", nil, false},
		{"unverified email", "email_verified", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims = map[string]any{"iss": server.URL, "sub": "stable-subject", "aud": "coconet-fixture", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": "expected-nonce", "name": "Private developer", "email": "developer@private.test", "email_verified": true}
			invalidSignature = test.field == "signature"
			if test.field != "" {
				claims[test.field] = test.value
			}
			subject, name, email, err := provider.exchange(ctx, "fixture-code", oauth2.GenerateVerifier(), "expected-nonce")
			if test.accept {
				expectedEmail := "developer@private.test"
				if test.field == "email_verified" {
					expectedEmail = ""
				}
				if err != nil || subject != "stable-subject" || name != "Private developer" || email != expectedEmail {
					t.Fatal("valid OIDC login failed", err)
				}
			} else if err == nil {
				t.Fatal("invalid OIDC identity accepted")
			}
		})
	}
}
