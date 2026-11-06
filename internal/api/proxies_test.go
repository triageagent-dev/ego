package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedProxiesNameTheClientBehindTheGateway(t *testing.T) {
	p, err := parseTrustedProxies("10.0.0.0/8, 2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct{ remote, xff, want string }{
		// From the gateway: the nearest hop it did not add itself is the client.
		{"10.1.2.3:4444", "203.0.113.9", "203.0.113.9"},
		{"10.1.2.3:4444", "198.51.100.7, 203.0.113.9, 10.9.9.9", "203.0.113.9"},
		{"[2001:db8::1]:443", "203.0.113.9:1234", "203.0.113.9"},
		// Every hop trusted: the first one, rather than nothing.
		{"10.1.2.3:4444", "10.5.5.5, 10.6.6.6", "10.5.5.5"},
		// From the gateway with no header: the gateway.
		{"10.1.2.3:4444", "", "10.1.2.3"},
		// Not from the gateway: the header is the client's own claim and is ignored.
		{"203.0.113.9:4444", "10.1.2.3", "203.0.113.9"},
	}
	for _, c := range cases {
		if got := p.source(req(c.remote, c.xff)); got != c.want {
			t.Errorf("source(%s, xff %q) = %q, want %q", c.remote, c.xff, got, c.want)
		}
	}

	var none *trustedProxies
	if got := none.source(req("10.1.2.3:4444", "203.0.113.9")); got != "10.1.2.3" {
		t.Errorf("with no trusted proxies the header was used: %q", got)
	}
	if p, err := parseTrustedProxies(" , "); err != nil || p != nil {
		t.Errorf("an empty list = %v, %v", p, err)
	}
	if _, err := parseTrustedProxies("gateway"); err == nil {
		t.Error("a name was accepted as a proxy address")
	}
}
