package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// Request limits. The OTLP receivers and the health port take unauthenticated traffic, so every body, header
// and connection they accept is bounded.
const (
	// maxOTLPBody caps an OTLP/HTTP body as received; maxOTLPDecoded caps it after gzip is undone, so a small
	// compressed payload cannot expand without bound.
	maxOTLPBody    = 16 << 20
	maxOTLPDecoded = 64 << 20

	// maxJSONBody caps the JSON bodies of the API's write endpoints; maxUploadBody caps a multipart upload
	// (and so what spills to temp files).
	maxJSONBody   = 8 << 20
	maxUploadBody = 64 << 20

	maxListLimit = 1000
)

var errTooLarge = errors.New("request body too large")

// capReader reads from r and fails with errTooLarge once more than limit bytes have been read.
type capReader struct {
	r     io.Reader
	left  int64
	close func() error
}

func newCapReader(r io.Reader, limit int64, closer func() error) *capReader {
	return &capReader{r: r, left: limit, close: closer}
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errTooLarge
	}
	return n, err
}

func (c *capReader) Close() error {
	if c.close != nil {
		return c.close()
	}
	return nil
}

// isTooLarge reports whether err came from a body limit, from capReader or from http.MaxBytesReader.
func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.Is(err, errTooLarge) || errors.As(err, &mbe)
}

// decodeJSON reads one JSON value of at most maxJSONBody bytes from the request.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	return json.NewDecoder(r.Body).Decode(v)
}

// limitUpload bounds a multipart upload before it is parsed. w may be nil.
func limitUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
}

// clampLimit returns a positive ?limit= capped at maxListLimit, or def when absent or invalid.
func clampLimit(v, def int) int {
	if v <= 0 {
		return def
	}
	if v > maxListLimit {
		return maxListLimit
	}
	return v
}

// newHTTPServer returns a server with timeouts. readTimeout bounds reading a whole request and writeTimeout a
// whole response; pass 0 for a listener that streams (server-sent events, websockets, long evaluations).
func newHTTPServer(addr string, h http.Handler, readTimeout, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// securityHeaders adds the response headers that keep the UI from being framed, sniffed or loaded from
// elsewhere. The CSP allows the inline styles the UI's CSS-in-JS injects and the optional classic-mode fonts.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self'; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src 'self' data: https://fonts.gstatic.com; img-src 'self' data:; " +
		"connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}
