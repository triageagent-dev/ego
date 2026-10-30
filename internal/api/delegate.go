package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/mcptools"
)

// A delegate token (token.go) belongs to a client that serves many tenants and must not read across them: a core
// answering one workspace's question holds one credential for all of them. The credential carries the tenants the
// client may act for; each tools/call names the one it acts for now, in the `tenant` argument, and ego narrows that
// call to it, for every tool whether or not the tool declares a tenant argument. A call that names none, a tenant
// outside the token's grants, or a pattern is refused as invalid params and never reaches a tool.
//
// A workspace is one tenant label, but its data can arrive under several senders: the core files its own sessions as
// "<core>/<label>", and the spots working for it file theirs as "<spots>/<label>". A grant "<sender>/=" says "the
// call's own label under <sender>": a call for "<core>/acme" then also reads "<spots>/acme", and never another label
// of that sender. It grants nothing on its own, so a token holding it still cannot name a tenant of that sender.

const jsonRPCInvalidParams = -32602

// sameLabelSuffix ends a delegate grant that follows the call's label into another sender. "=" is not a character a
// tenant label can hold (ValidTenantLabel), so the grant never matches a real tenant key.
const sameLabelSuffix = "/="

// delegateMethods are the requests a delegate may send without naming a tenant: they carry no tenant data.
var delegateMethods = map[string]bool{
	"initialize":                true,
	"notifications/initialized": true,
	"notifications/cancelled":   true,
	"ping":                      true,
	"tools/list":                true,
	"prompts/list":              true,
	"prompts/get":               true,
}

// delegateRoleInfo is the access of a delegate token before a call names its tenant. It fails closed: a token with
// no tenants gets an empty (not nil, which means every tenant) set, and one with a role this build does not know is a
// viewer, so a malformed token reads less, never more.
func delegateRoleInfo(id identity) RoleInfo {
	role, err := ParseRole(string(id.Role))
	if err != nil {
		role = RoleViewer
	}
	return RoleInfo{Role: role, Tenants: append([]string{}, id.Tenants...)}
}

// delegateTenant wraps the MCP handler: a request from any other identity passes through unchanged.
func delegateTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := identityFromContext(r.Context())
		if !ok || id.Kind != tokenKindDelegate {
			next.ServeHTTP(w, r)
			return
		}
		// Nothing but the endpoint's POST carries a call; the transport answers the other methods itself.
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcptools.MaxRequestBytes))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "reading the request body failed", http.StatusBadRequest)
			}
			return
		}

		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Arguments map[string]json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			// A batch, or anything that is not one request: refused here so no element escapes the check.
			writeRPCError(w, nil, -32600, "a delegate token sends one JSON-RPC request per POST")
			return
		}
		if req.Method != "tools/call" {
			if !delegateMethods[req.Method] {
				writeRPCError(w, req.ID, -32601, "method not available to a delegate token")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
			return
		}

		var tenant string
		if raw, present := req.Params.Arguments["tenant"]; present {
			_ = json.Unmarshal(raw, &tenant)
		}
		reason := ""
		switch {
		case tenant == "":
			reason = "tenant is required: a delegate token acts for one tenant per call, so pass its key as the `tenant` argument"
		case strings.Contains(tenant, "*"):
			reason = "tenant must be one tenant key, not a pattern"
		case !tenantGranted(id.Tenants, tenant):
			reason = "tenant is not one this token may act for"
		}
		if reason != "" {
			logx.Warnf("delegate token %s refused: %s (tenant %q)", id.TokenID, strings.SplitN(reason, ":", 2)[0], clip(tenant, 128))
			writeRPCError(w, req.ID, jsonRPCInvalidParams, reason)
			return
		}

		// The role the token was minted with, limited to this call's tenant (and its label under the senders the token
		// follows it into) and nothing wider.
		tenants := callTenants(id.Tenants, tenant)
		if len(tenants) > 1 {
			// A tool's own tenant filter would pin the call back to the one key it names; the role already holds the set.
			if body, err = withoutTenantArgument(body); err != nil {
				writeRPCError(w, req.ID, -32600, "unreadable request")
				return
			}
		}
		ctx := withRoleInfo(r.Context(), RoleInfo{Role: delegateRoleInfo(id).Role, Tenants: tenants})
		r = r.WithContext(ctx)
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// callTenants is the tenants one call may read: the key it names, then its label under each sender a "<sender>/="
// grant follows it into. A key with no sender (the shared ingest token's) or with a label that cannot be a tenant's
// has no other senders to follow.
func callTenants(grants []string, key string) []string {
	out := []string{key}
	i := strings.Index(key, "/")
	if i <= 0 || ValidTenantLabel(key[i+1:]) != nil {
		return out
	}
	label := key[i+1:]
	for _, g := range grants {
		sender, ok := strings.CutSuffix(g, sameLabelSuffix)
		if !ok || sender == key[:i] || !senderIDPattern.MatchString(sender) {
			continue
		}
		if k := sender + "/" + label; !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// withoutTenantArgument drops params.arguments.tenant from a tools/call body, leaving everything else as sent.
func withoutTenantArgument(body []byte) ([]byte, error) {
	var req, params, args map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(req["params"], &params); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(params["arguments"], &args); err != nil {
		return nil, err
	}
	delete(args, "tenant")
	var err error
	if params["arguments"], err = json.Marshal(args); err != nil {
		return nil, err
	}
	if req["params"], err = json.Marshal(params); err != nil {
		return nil, err
	}
	return json.Marshal(req)
}

// tenantGranted reports whether any grant covers the tenant key. A follow-grant covers none: it only widens a call.
func tenantGranted(grants []string, key string) bool {
	for _, g := range grants {
		if !strings.HasSuffix(g, sameLabelSuffix) && tenantPatternMatches(g, key) {
			return true
		}
	}
	return false
}

// writeRPCError answers as a JSON-RPC error on a 200, which is how the transport reports a failed request.
func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}
