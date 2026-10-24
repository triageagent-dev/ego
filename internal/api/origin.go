package api

import (
	"net"
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

// listensOnLoopback reports that addr ("host:port") is bound to a loopback address or to localhost.
func listensOnLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackHostOnly answers only requests whose Host names the loopback interface (localhost, 127.0.0.1, ::1), and
// refuses any other name with 421, so a DNS-rebinding page cannot reach a server that has no login.
func loopbackHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if !strings.EqualFold(host, "localhost") {
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
