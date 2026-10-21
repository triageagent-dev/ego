package api

import (
	_ "embed"
	"net/http"
)

// signedOutPage is where logout lands: the logo alone, on the terminal background, as the link back to sign-in.
// It has no script and no state, so it is served without a session.
//
//go:embed signedout.html
var signedOutPage []byte

func signedOutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		_, _ = w.Write(signedOutPage)
	}
}
