package main

import (
	"fmt"
	"os"
	"strconv"
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
	return checkSecret("session secret", secret)
}

// checkSecret rejects a secret that is set but shorter than minSecretLen.
func checkSecret(what, secret string) error {
	if secret != "" && len(secret) < minSecretLen {
		return fmt.Errorf("%s is %d bytes, want at least %d (try: openssl rand -hex 32)", what, len(secret), minSecretLen)
	}
	return nil
}

// envInt64OrDefault reads an integer env var, falling back to def when it is unset or not a number.
func envInt64OrDefault(key string, def int64) int64 {
	if v, err := strconv.ParseInt(env(key), 10, 64); err == nil {
		return v
	}
	return def
}
