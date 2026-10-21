package mcptools

import (
	"net/http"

	"github.com/mark3labs/mcp-go/server"
)

// NewHTTPHandler serves the registry over the MCP streamable HTTP transport (POST /mcp), for `serve`.
//
// It is stateless: no session id is issued and every request stands alone, so the replicas of a deployment
// need no shared state and the caller's role, put in the request context by the auth layer in front, is the
// one the tool call runs with. The standalone GET stream is off (405, which the transport allows): nothing is
// ever pushed to a client, and a gateway that buffers long-lived streams would only hold the connection.
// DELETE has no session to end and is answered by the transport as such.
func NewHTTPHandler(b Backend, o Options) http.Handler {
	return server.NewStreamableHTTPServer(NewServer(b, o),
		server.WithStateLess(true),
		server.WithDisableStreaming(true),
	)
}
