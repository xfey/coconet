package main

import (
	"crypto/rand"
	"encoding/base64"
)

func newRequestID() string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "req_runtime_fallback"
	}
	return "req_" + base64.RawURLEncoding.EncodeToString(raw)
}
