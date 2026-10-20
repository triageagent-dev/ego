package api

import (
	"net/http"
	"net/url"
	"strings"
)

// originGuard refuses a browser request that another site caused. A page on any origin can make the user's
// browser send a form POST or open a WebSocket to this server, and the cookie (or, with no login, the open
// port) goes along, so state-changing requests and WebSocket upgrades must come from this server's own origin.
// Requests with neither Origin nor Sec-Fetch-Site (curl, the CLI, the MCP server) are not browser-driven and pass.
//
// publicHost is the host of the configured public URL, accepted in addition to the Host the request arrived on
// (a gateway may rewrite Host; X-Forwarded-Host is accepted for the same reason).
func originGuard(publicHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mustCheckOrigin(r) && !originAllowed(r, publicHost) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mustCheckOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	}
	return true
}

func originAllowed(r *http.Request, publicHost string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "cross-site", "same-site":
			return false
		}
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // "null" (sandboxed frame, redirect chain) or malformed.
	}
	for _, h := range []string{r.Host, r.Header.Get("X-Forwarded-Host"), publicHost} {
		if h != "" && strings.EqualFold(u.Host, h) {
			return true
		}
	}
	return false
}
