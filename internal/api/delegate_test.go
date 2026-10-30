package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

const delegateTestSecret = "a-session-secret-that-is-long-enough"

type rpcReply struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	ID     json.RawMessage `json:"id"`
	Result mcpToolResult   `json:"result"`
}

// rpc posts one JSON-RPC request to h as a bearer token and returns the status and the decoded reply.
func rpc(t *testing.T, h http.Handler, token, path string, payload any) (int, rpcReply) {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var reply rpcReply
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	return rec.Code, reply
}

func toolCall(id int, name string, args map[string]any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
}

func delegateHandler(t *testing.T) (http.Handler, *mcpFixture) {
	t.Helper()
	f := newMCPFixture(t)
	roles, err := NewRoleStore(nil, RoleNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mcpRoute(delegateTestSecret, nil, roles, "", nil, nil, f.backend), f
}

func mintDelegate(t *testing.T, role Role, tenants ...string) (string, string) {
	t.Helper()
	tok, id, err := MintDelegateToken("core", delegateTestSecret, role, tenants, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok, id
}

func alertTenants(t *testing.T, reply rpcReply) []string {
	t.Helper()
	if reply.Error != nil || reply.Result.IsError {
		t.Fatalf("call failed: %+v", reply)
	}
	var q mcptools.Quality
	if err := json.Unmarshal(reply.Result.StructuredContent, &q); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, a := range q.OpenAlerts {
		out = append(out, a.Tenant)
	}
	return out
}

// Each call reads the tenant it names and nothing else, though the token covers two senders.
func TestDelegateToken_NarrowsEveryCallToTheNamedTenant(t *testing.T) {
	h, f := delegateHandler(t)
	tok, _ := mintDelegate(t, RoleMember, "core/*", "rc/*")

	for _, tenant := range []string{"core/acme", "rc/acme"} {
		code, reply := rpc(t, h, tok, "/mcp", toolCall(1, "get_quality", map[string]any{"tenant": tenant}))
		if got := alertTenants(t, reply); code != http.StatusOK || len(got) != 1 || got[0] != tenant {
			t.Errorf("get_quality for %s = %d %v", tenant, code, got)
		}
	}

	// get_run_results declares no tenant argument; the narrowing still reaches it.
	if _, reply := rpc(t, h, tok, "/mcp", toolCall(2, "get_run_results", map[string]any{"tenant": "core/acme", "run_id": f.runOf["rc/acme"]})); !reply.Result.IsError {
		t.Error("a call for core/acme read a run of rc/acme")
	}
	if _, reply := rpc(t, h, tok, "/mcp", toolCall(3, "get_run_results", map[string]any{"tenant": "core/acme", "run_id": f.runOf["core/acme"]})); reply.Error != nil || reply.Result.IsError {
		t.Errorf("a call for core/acme refused its own run: %+v", reply)
	}
	// The delegate is a member whatever it names: the admin tools stay shut.
	if _, reply := rpc(t, h, tok, "/mcp", toolCall(4, "list_role_bindings", map[string]any{"tenant": "core/acme"})); !reply.Result.IsError {
		t.Error("a delegate token listed role bindings")
	}
	admin, _ := mintDelegate(t, RoleAdmin, "core/*")
	if _, reply := rpc(t, h, admin, "/mcp", toolCall(5, "list_role_bindings", map[string]any{"tenant": "core/acme"})); !reply.Result.IsError {
		t.Error("an admin-role delegate token listed role bindings: a tenant-limited admin manages nothing")
	}
}

// A "<sender>/=" grant follows the call's label into that sender, for that label only. The tool's own tenant argument
// would pin the call back to the one key, so it must not reach the tool.
func TestDelegateToken_SameLabelGrantFollowsTheLabelIntoAnotherSender(t *testing.T) {
	h, _ := delegateHandler(t)
	tok, _ := mintDelegate(t, RoleViewer, "core/*", "rc/=")

	_, reply := rpc(t, h, tok, "/mcp", toolCall(1, "get_quality", map[string]any{"tenant": "core/acme"}))
	got := alertTenants(t, reply)
	slices.Sort(got)
	if !slices.Equal(got, []string{"core/acme", "rc/acme"}) {
		t.Errorf("a call for core/acme read %v, want core/acme and rc/acme", got)
	}

	// Another label of the followed sender stays shut, and the follow-grant names no tenant of its own.
	_, reply = rpc(t, h, tok, "/mcp", toolCall(2, "get_quality", map[string]any{"tenant": "core/other"}))
	if got := alertTenants(t, reply); len(got) != 0 {
		t.Errorf("a call for core/other read %v", got)
	}
	for _, tenant := range []string{"rc/acme", "rc/=", "core/="} {
		_, reply = rpc(t, h, tok, "/mcp", toolCall(3, "get_quality", map[string]any{"tenant": tenant}))
		if tenant != "core/=" && (reply.Error == nil || reply.Error.Code != jsonRPCInvalidParams) {
			t.Errorf("tenant %s: %+v, want invalid params", tenant, reply)
		}
		if tenant == "core/=" {
			if got := alertTenants(t, reply); len(got) != 0 {
				t.Errorf("tenant %s read %v", tenant, got)
			}
		}
	}

	// Without the grant the same call reads its own key only.
	plain, _ := mintDelegate(t, RoleViewer, "core/*")
	_, reply = rpc(t, h, plain, "/mcp", toolCall(4, "get_quality", map[string]any{"tenant": "core/acme"}))
	if got := alertTenants(t, reply); len(got) != 1 || got[0] != "core/acme" {
		t.Errorf("without a follow-grant a call for core/acme read %v", got)
	}
}

func TestDelegateToken_MintNeedsAGrantTheCallCanName(t *testing.T) {
	if _, _, err := MintDelegateToken("core", delegateTestSecret, RoleViewer, []string{"rc/="}, time.Hour); err == nil {
		t.Error("a delegate token holding only a follow-grant was minted")
	}
}

func TestCallTenants(t *testing.T) {
	grants := []string{"core/*", "spots/=", "rc/=", "core/="}
	for _, c := range []struct {
		key  string
		want []string
	}{
		{"core/acme", []string{"core/acme", "spots/acme", "rc/acme"}},
		{"spots/acme", []string{"spots/acme", "rc/acme", "core/acme"}},
		{"acme", []string{"acme"}},
		{"core/a=b", []string{"core/a=b"}},
	} {
		if got := callTenants(grants, c.key); !slices.Equal(got, c.want) {
			t.Errorf("callTenants(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestWithoutTenantArgument(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_ego_tools","arguments":{"tenant":"core/acme","hours":24}}}`)
	out, err := withoutTenantArgument(body)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID     int `json:"id"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got.Params.Arguments["tenant"]; has || got.Params.Arguments["hours"] != float64(24) || got.ID != 7 || got.Params.Name != "get_ego_tools" {
		t.Errorf("rewritten call = %s", out)
	}
}

func TestDelegateToken_RefusesACallWithoutAValidTenant(t *testing.T) {
	h, _ := delegateHandler(t)
	tok, id := mintDelegate(t, RoleMember, "core/*")

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	cases := map[string]map[string]any{
		"missing":          {},
		"empty":            {"tenant": ""},
		"not a string":     {"tenant": 7},
		"pattern":          {"tenant": "core/*"},
		"every tenant":     {"tenant": "*"},
		"another sender":   {"tenant": "rc/acme"},
		"a bare label":     {"tenant": "acme"},
		"sender prefix":    {"tenant": "core"},
		"lookalike sender": {"tenant": "core-x/acme"},
	}
	for name, args := range cases {
		code, reply := rpc(t, h, tok, "/mcp", toolCall(9, "get_quality", args))
		if code != http.StatusOK || reply.Error == nil || reply.Error.Code != -32602 {
			t.Errorf("%s: got %d %+v, want JSON-RPC -32602", name, code, reply)
			continue
		}
		if string(reply.ID) != "9" {
			t.Errorf("%s: reply id %s, want 9", name, reply.ID)
		}
	}

	_, reply := rpc(t, h, tok, "/mcp", toolCall(1, "get_quality", nil))
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "tenant is required") {
		t.Errorf("missing tenant message = %+v", reply.Error)
	}
	_, reply = rpc(t, h, tok, "/mcp", toolCall(1, "get_quality", map[string]any{"tenant": "rc/acme"}))
	if reply.Error == nil || reply.Error.Message != "tenant is not one this token may act for" {
		t.Errorf("foreign tenant message = %+v", reply.Error)
	}
	_, reply = rpc(t, h, tok, "/mcp", toolCall(1, "get_quality", map[string]any{"tenant": "core/*"}))
	if reply.Error == nil || reply.Error.Message != "tenant must be one tenant key, not a pattern" {
		t.Errorf("pattern message = %+v", reply.Error)
	}

	// The refusal is logged with the token id and the tenant tried, and not the call's other arguments.
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, id) || !strings.Contains(out, `tenant \"rc/acme\"`) {
		t.Errorf("rejected call not logged at warn with token id and tenant:\n%s", out)
	}
	_, _ = rpc(t, h, tok, "/mcp", toolCall(1, "get_run_results", map[string]any{"tenant": "rc/acme", "run_id": "secret-run-argument"}))
	if strings.Contains(logs.String(), "secret-run-argument") {
		t.Error("the log carries a call argument")
	}
}

func TestDelegateToken_OneRequestPerPost(t *testing.T) {
	h, _ := delegateHandler(t)
	tok, _ := mintDelegate(t, RoleMember, "core/*")

	// A batch is how a second, unchecked call would ride in behind a valid one.
	batch := []any{toolCall(1, "get_quality", map[string]any{"tenant": "core/acme"}), toolCall(2, "get_quality", map[string]any{"tenant": "rc/acme"})}
	if _, reply := rpc(t, h, tok, "/mcp", batch); reply.Error == nil || reply.Error.Code != -32600 {
		t.Errorf("batch = %+v, want -32600", reply)
	}

	// Requests that carry no tenant data need no tenant; anything else unknown is refused.
	for _, method := range []string{"initialize", "ping", "tools/list"} {
		req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{}}
		if method == "initialize" {
			req["params"] = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}
		}
		if code, reply := rpc(t, h, tok, "/mcp", req); code != http.StatusOK || reply.Error != nil {
			t.Errorf("%s = %d %+v", method, code, reply)
		}
	}
	if _, reply := rpc(t, h, tok, "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "resources/read", "params": map[string]any{}}); reply.Error == nil || reply.Error.Code != -32601 {
		t.Errorf("resources/read = %+v, want -32601", reply)
	}
}

// Every other credential behaves as it did.
func TestDelegateToken_LeavesOtherTokensAlone(t *testing.T) {
	h, _ := delegateHandler(t)

	// An unrestricted admin API token needs no tenant and sees every tenant.
	admin, _ := MintAPIToken("ops", delegateTestSecret, RoleAdmin, nil, time.Hour)
	if _, reply := rpc(t, h, admin, "/mcp", toolCall(1, "get_quality", nil)); len(alertTenants(t, reply)) != 2 {
		t.Errorf("admin token without a tenant saw %v", alertTenants(t, reply))
	}
	// A tenant-limited API token is limited as before, and a tenant argument is just the tool's filter.
	scoped, _ := MintAPIToken("ops", delegateTestSecret, RoleMember, []string{"core/*"}, time.Hour)
	if _, reply := rpc(t, h, scoped, "/mcp", toolCall(1, "get_quality", nil)); len(alertTenants(t, reply)) != 1 {
		t.Errorf("scoped token saw %v", alertTenants(t, reply))
	}
}

func TestDelegateToken_OpensMCPOnly(t *testing.T) {
	tok, _ := mintDelegate(t, RoleMember, "core/*")
	api := requireSession(delegateTestSecret, nil, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, path := range []string{"/api/runs", "/api/online/quality", "/stream/ui-updates"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		api.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("delegate token on %s = %d, want 401", path, rec.Code)
		}
	}
}

func TestDelegateToken_RevokedIsRefused(t *testing.T) {
	f := newMCPFixture(t)
	roles, _ := NewRoleStore(nil, RoleNone, nil)
	revoked, _ := newRevocations(nil)
	h := mcpRoute(delegateTestSecret, nil, roles, "", nil, revoked, f.backend)
	tok, id := mintDelegate(t, RoleMember, "core/*")
	if code, _ := rpc(t, h, tok, "/mcp", toolCall(1, "get_quality", map[string]any{"tenant": "core/acme"})); code != http.StatusOK {
		t.Fatalf("before revocation: %d", code)
	}
	if err := revoked.RevokeToken(id, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if code, _ := rpc(t, h, tok, "/mcp", toolCall(1, "get_quality", map[string]any{"tenant": "core/acme"})); code != http.StatusUnauthorized {
		t.Errorf("revoked delegate token = %d, want 401", code)
	}
}

func TestDelegateToken_MintAndParse(t *testing.T) {
	if _, _, err := MintDelegateToken("core", delegateTestSecret, RoleMember, nil, time.Hour); err == nil {
		t.Error("a delegate token without tenants was minted")
	}
	if _, _, err := MintDelegateToken("core", delegateTestSecret, RoleMember, []string{"core/*", "*"}, time.Hour); err == nil {
		t.Error("a delegate token for every tenant was minted")
	}
	tok, id := mintDelegate(t, RoleViewer, "core/*", "rc/acme")
	got, ok := parseToken(tok, delegateTestSecret)
	if !ok || got.Kind != tokenKindDelegate || got.ID != id || got.Role != RoleViewer || len(got.Tenants) != 2 {
		t.Errorf("parsed delegate token = %+v ok=%v", got, ok)
	}
	// A signed delegate payload with no tenants is not a token.
	forged := authToken{Kind: tokenKindDelegate, ID: "tok_x", Username: "x", Role: RoleMember, IssuedAt: time.Now().Unix(), Expiry: time.Now().Add(time.Hour).Unix()}
	if _, ok := parseToken(forged.encode(delegateTestSecret), delegateTestSecret); ok {
		t.Error("a delegate token without tenants parsed")
	}
	// Tokens minted before the kind existed still parse.
	api, _ := MintAPIToken("ops", delegateTestSecret, RoleAdmin, nil, time.Hour)
	if got, ok := parseToken(api, delegateTestSecret); !ok || got.Kind != tokenKindAPI {
		t.Errorf("API token = %+v ok=%v", got, ok)
	}
}

func TestDelegateRoleInfo_FailsClosed(t *testing.T) {
	if got := delegateRoleInfo(identity{Role: RoleMember}); got.Tenants == nil || len(got.Tenants) != 0 {
		t.Errorf("a delegate token without tenants reads %v, want the empty set", got.Tenants)
	}
	if got := delegateRoleInfo(identity{Role: Role("owner"), Tenants: []string{"a/*"}}); got.Role != RoleViewer {
		t.Errorf("an unknown role became %q, want viewer", got.Role)
	}
	if got := delegateRoleInfo(identity{Role: RoleAdmin, Tenants: []string{"a/*"}}); got.Role != RoleAdmin || len(got.Tenants) != 1 {
		t.Errorf("a valid token changed: %+v", got)
	}
}
