package vertexeval

import "testing"

func TestGoogleErrorOf(t *testing.T) {
	got := googleErrorOf([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"turns must not be empty"}}`))
	if want := "INVALID_ARGUMENT: turns must not be empty"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := googleErrorOf([]byte(`<html>bad gateway</html>`)); got != "" {
		t.Errorf("a body that is not the envelope gave %q, want nothing", got)
	}
}
