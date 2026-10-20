package main

import (
	"fmt"
	"net"
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

// envBool reads a boolean env var; unset or unparsable is false.
func envBool(key string) bool {
	v, err := strconv.ParseBool(env(key))
	return err == nil && v
}

// checkExposure refuses to serve without a login on addresses other hosts can reach. Without a session secret
// every caller is an admin who can spend judge budget and change roles, so that has to be asked for (--no-auth),
// not arrive by leaving a setting out; an empty secret from a Secret key that was never filled in lands here too.
func checkExposure(secret string, noAuth bool, addrs ...string) error {
	if secret != "" {
		if noAuth {
			return fmt.Errorf("--no-auth contradicts --session-secret: choose one")
		}
		return nil
	}
	if noAuth {
		return nil
	}
	for _, a := range addrs {
		if !isLoopbackAddr(a) {
			return fmt.Errorf("refusing to listen on %q without a login: set --session-secret (EGO_SESSION_SECRET), or --no-auth (EGO_NO_AUTH=true) to serve without one, or listen on 127.0.0.1", a)
		}
	}
	return nil
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
