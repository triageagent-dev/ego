package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A token bound to a tenant sends into that tenant only, whatever label its spans carry, and reads that tenant only.
func TestTenantBoundTokenKeepsToTheTenant(t *testing.T) {
	ss, _ := NewSenderStore(nil)
	if _, err := ss.Create("demo", ""); err != nil {
		t.Fatal(err)
	}
	token, meta, err := ss.NewToken("demo", "ci", "t1")
	if err != nil || meta.Tenant != "t1" {
		t.Fatalf("token = %+v err=%v", meta, err)
	}
	if tok, ok := ss.Resolve(token); !ok || tok.Sender != "demo" || tok.Tenant != "t1" {
		t.Fatalf("resolve = %+v %v, want demo bound to t1", tok, ok)
	}
	if _, _, err := ss.NewToken("demo", "", "a b"); err == nil {
		t.Error("a tenant with a space was accepted")
	}

	store := NewSessionStore(nil)
	// The spans claim another tenant of the sender; the token's binding wins. An unbound token's spans label themselves.
	store.IngestBound("demo", "t1", labelledTracesBody("a", "aaaa", "s1", "other"))
	store.IngestBound("demo", "", labelledTracesBody("b", "bbbb", "s2", "other"))
	got := map[string][]string{}
	for _, sum := range store.List() {
		got[sum.SessionID] = sum.Tenants
	}
	if len(got) != 2 {
		t.Fatalf("sessions = %v", got)
	}
	var bound, free int
	for id, tenants := range got {
		switch {
		case strings.Contains(id, "t1"):
			bound++
			if len(tenants) != 1 || tenants[0] != "demo/t1" {
				t.Errorf("bound session %s tenants = %v, want [demo/t1]", id, tenants)
			}
		case len(tenants) == 1 && tenants[0] == "demo/other":
			free++
		}
	}
	if bound != 1 || free != 1 {
		t.Errorf("sessions = %v, want one under demo/t1 and one under demo/other", got)
	}

	// On /mcp the bound token reads its tenant only; an unbound one reads all the sender's.
	bound1 := roleForIdentity(nil, nil, "", nil, identity{Kind: identitySender, Sender: "demo", SenderTenant: "t1"})
	if len(bound1.Tenants) != 1 || bound1.Tenants[0] != "demo/t1" {
		t.Errorf("bound token tenants = %v", bound1.Tenants)
	}
	if all := roleForIdentity(nil, nil, "", nil, identity{Kind: identitySender, Sender: "demo"}); len(all.Tenants) != 1 || all.Tenants[0] != "demo/*" {
		t.Errorf("unbound token tenants = %v", all.Tenants)
	}
}

// Minting a token with a tenant and a name names the tenant in the same step; a bad request leaves nothing behind.
func TestMintTenantBoundTokenNamesTheTenant(t *testing.T) {
	ss, _ := NewSenderStore(nil)
	if _, err := ss.Create("demo", ""); err != nil {
		t.Fatal(err)
	}
	names, _ := NewTenantNames(nil)
	h := adminSenderTokensHandler(ss, names)
	admin := withRoleInfoForTest(RoleInfo{Role: RoleAdmin, IsAdmin: true})
	post := func(body string) int {
		req := admin(httptest.NewRequest(http.MethodPost, "/api/admin/senders/demo/tokens", strings.NewReader(body)))
		req.SetPathValue("id", "demo")
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	if code := post(`{"tenant":"t1","tenantName":"Demo One"}`); code != http.StatusOK {
		t.Fatalf("mint = %d", code)
	}
	if got := names.Name("demo/t1"); got != "Demo One" {
		t.Errorf("name = %q, want Demo One", got)
	}
	for _, bad := range []string{`{"tenantName":"No tenant"}`, `{"tenant":"a b"}`, `{"tenant":"t2","tenantName":"bad\u0000name"}`} {
		if code := post(bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	if n := len(ss.Tokens("demo")); n != 1 {
		t.Errorf("tokens = %d, a refused request must not leave a token", n)
	}
}
