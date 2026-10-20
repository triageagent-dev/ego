package main

import (
	"fmt"
	"os"
)

// env reads a setting from EGO_<key>.
func env(key string) string {
	return os.Getenv("EGO_" + key)
}

// minSecretLen is the shortest signing secret accepted: cookies and tokens are HMAC-SHA256 under it, so a short
// one can be guessed offline from a single captured value.
const minSecretLen = 32

// checkSessionSecret rejects a secret that is set but too short. An empty secret is allowed here; it means
// authentication is off.
func checkSessionSecret(secret string) error {
	if secret != "" && len(secret) < minSecretLen {
		return fmt.Errorf("session secret is %d bytes, want at least %d (try: openssl rand -hex 32)", len(secret), minSecretLen)
	}
	return nil
}
