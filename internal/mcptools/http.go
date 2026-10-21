package mcptools

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/mark3labs/mcp-go/server"
)

// MaxRequestBytes caps a POST /mcp body. A tool call is a few hundred bytes; the transport reads the whole body
// into memory, so without a cap any caller (an ingest token is enough) could hold the process's memory.
const MaxRequestBytes = 1 << 20

// NewHTTPHandler serves the registry over the MCP streamable HTTP transport (POST /mcp), for `serve`.
//
// It is stateless: no session id is issued and every request stands alone, so the replicas of a deployment
// need no shared state and the caller's role, put in the request context by the auth layer in front, is the
// one the tool call runs with. The standalone GET stream is off (405, which the transport allows): nothing is
// ever pushed to a client, and a gateway that buffers long-lived streams would only hold the connection.
// DELETE has no session to end and is answered by the transport as such.
func NewHTTPHandler(b Backend, o Options) http.Handler {
	return limitBody(server.NewStreamableHTTPServer(NewServer(b, o),
		server.WithStateLess(true),
		server.WithDisableStreaming(true),
	), MaxRequestBytes)
}

// limitBody refuses a POST body over max with 413. The body is read here, under http.MaxBytesReader, and handed
// on as a small in-memory reader: the transport reads with io.ReadAll and would itself answer an oversized body
// with a JSON-RPC parse error, not a status the client can act on.
func limitBody(next http.Handler, max int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil {
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
			if err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "reading the request body failed", http.StatusBadRequest)
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		next.ServeHTTP(w, r)
	})
}
