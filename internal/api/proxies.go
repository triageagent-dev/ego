package api

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// trustedProxies are the addresses of the gateways in front of ego. A request that arrives from one of them names its
// client in X-Forwarded-For, so the per-source refusal counters (ingest tokens, GitHub tokens) count the client and
// not the gateway that every client shares. From any other address the header is the client's own claim and is
// ignored. nil trusts nobody.
type trustedProxies struct {
	nets []*net.IPNet
}

// parseTrustedProxies reads a comma-separated list of CIDRs or addresses; "" is nobody.
func parseTrustedProxies(s string) (*trustedProxies, error) {
	var p trustedProxies
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "/") {
			ip := net.ParseIP(item)
			if ip == nil {
				return nil, fmt.Errorf("trusted proxy %q is not an address or a CIDR", item)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			item = fmt.Sprintf("%s/%d", ip, bits)
		}
		_, n, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", item, err)
		}
		p.nets = append(p.nets, n)
	}
	if len(p.nets) == 0 {
		return nil, nil
	}
	return &p, nil
}

func (p *trustedProxies) trusted(host string) bool {
	if p == nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range p.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// maxSourceLen bounds a source taken from a header, so a forged hop cannot fill the counters with long keys.
const maxSourceLen = 64

// source is the client address a request came from: the nearest X-Forwarded-For hop that is not a trusted proxy
// when the request arrived from one, else the peer address itself.
func (p *trustedProxies) source(r *http.Request) string {
	host := sourceOf(r.RemoteAddr)
	if !p.trusted(host) {
		return host
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, sourceOf(h))
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !p.trusted(hops[i]) {
			return clipSource(hops[i])
		}
	}
	if len(hops) > 0 {
		return clipSource(hops[0])
	}
	return host
}

func clipSource(s string) string {
	if len(s) > maxSourceLen {
		return s[:maxSourceLen]
	}
	return s
}
