package main

import "testing"

func TestCheckExposure(t *testing.T) {
	loop := []string{"127.0.0.1:8001", "localhost:4318", "[::1]:4317"}
	open := []string{"127.0.0.1:8001", ":4318"}
	for name, tc := range map[string]struct {
		secret  string
		noAuth  bool
		addrs   []string
		wantErr bool
	}{
		"loopback without login":   {"", false, loop, false},
		"all interfaces, no login": {"", false, open, true},
		"wildcard host":            {"", false, []string{"0.0.0.0:8001"}, true},
		"named host, no login":     {"", false, []string{"ego.example.com:8001"}, true},
		"explicit no-auth":         {"", true, open, false},
		"secret set":               {"0123456789abcdef0123456789abcdef", false, open, false},
		"secret and no-auth":       {"0123456789abcdef0123456789abcdef", true, loop, true},
		"unparsable address":       {"", false, []string{"8001"}, true},
	} {
		if err := checkExposure(tc.secret, tc.noAuth, tc.addrs...); (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", name, err, tc.wantErr)
		}
	}
}
