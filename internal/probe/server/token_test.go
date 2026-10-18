package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dihedron/lxd-spiffe/internal/probe"
)

// jwtOf builds a compact JWT from its parts, unsigned unless sig is given.
func jwtOf(header, claims, sig string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(header)) + "." + enc([]byte(claims)) + "." + sig
}

// TestBearerTokenRefusesUnsigned: LXD issues signed tokens only, so a token
// with alg "none" or no signature is a mistake, refused before any request
// (the probe never verifies a signature: LXD does, on every call).
func TestBearerTokenRefusesUnsigned(t *testing.T) {
	signed := jwtOf(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"lab-bearer"}`, "c2lnbmF0dXJl")
	for name, tt := range map[string]struct {
		token string
		ok    bool
	}{
		"signed":              {signed, true},
		"alg none":            {jwtOf(`{"alg":"none","typ":"JWT"}`, `{"sub":"x"}`, ""), false},
		"alg None, signature": {jwtOf(`{"alg":"None","typ":"JWT"}`, `{"sub":"x"}`, "c2ln"), false},
		"no signature":        {jwtOf(`{"alg":"HS256","typ":"JWT"}`, `{"sub":"x"}`, ""), false},
		"not a JWT":           {"not-a-token", false},
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(file, []byte(tt.token), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := bearerToken(&probe.ServerCommand{Auth: "bearer", TokenFile: file})
			if (err == nil) != tt.ok {
				t.Errorf("error %v, want ok=%v", err, tt.ok)
			}
			if err != nil && strings.Contains(err.Error(), tt.token) {
				t.Errorf("the error carries the token: %v", err)
			}
		})
	}
}
