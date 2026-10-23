package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTenantNamesPersistAndValidate(t *testing.T) {
	path := t.TempDir() + "/n.db"
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	names, err := NewTenantNames(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := names.Set("rc/0123-abcd", "Checkout"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, name string }{
		{"", "x"},
		{"k", strings.Repeat("n", maxTenantNameLen+1)},
		{"a\nb", "x"},
		{"k", "bad\x00name"},
		{strings.Repeat("k", maxTenantKeyLen+1), "x"},
	} {
		if err := names.Set(tc.key, tc.name); err == nil {
			t.Errorf("Set(%q, %q) accepted", tc.key, tc.name)
		}
	}
	archive.Close()

	again, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewTenantNames(again)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Name("rc/0123-abcd"); got != "Checkout" {
		t.Errorf("after restart name = %q", got)
	}
	if err := reloaded.Set("rc/0123-abcd", ""); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Name("rc/0123-abcd"); got != "" {
		t.Errorf("cleared name = %q", got)
	}
}

func TestTenantNamesAdminAPIAndSwitcher(t *testing.T) {
	names, _ := NewTenantNames(nil)
	post := func(info RoleInfo, body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/tenant-names", strings.NewReader(body))
		adminTenantNamesHandler(names)(rec, withRoleInfoForTest(info)(req))
		return rec.Code
	}
	if code := post(scopedToTenants("rc/*"), `{"key":"rc/a","name":"A"}`); code != http.StatusForbidden {
		t.Errorf("limited role got %d, want 403", code)
	}
	if code := post(RoleInfo{Role: RoleAdmin, IsAdmin: true}, `{"key":"rc/a","name":"Alpha"}`); code != http.StatusOK {
		t.Errorf("admin got %d", code)
	}

	store := NewSessionStore(nil)
	store.IngestFor("rc", labelledTracesBody("a", "aaaa", "s1", "a"))
	rec := httptest.NewRecorder()
	tenantsHandler(store, names)(rec, withRoleInfoForTest(scopedToTenants("rc/*"))(httptest.NewRequest(http.MethodGet, "/api/tenants", nil)))
	var out struct{ Data []tenantOption }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 || out.Data[0].Key != "rc/a" || out.Data[0].Name != "Alpha" {
		t.Errorf("switcher options = %+v", out.Data)
	}
}

// namedTenantBody labels its spans through the resource, as an OTel config would: ego.tenant and ego.tenant.name.
func namedTenantBody(session, traceID, spanID, label, name string) map[string]any {
	body := labelledTracesBody(session, traceID, spanID, "")
	res := body["resourceSpans"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	attrs := res["attributes"].([]any)
	attrs = append(attrs, map[string]any{"key": DefaultTenantAttr, "value": map[string]any{"stringValue": label}})
	if name != "" {
		attrs = append(attrs, map[string]any{"key": TenantNameAttr, "value": map[string]any{"stringValue": name}})
	}
	res["attributes"] = attrs
	return body
}

// A named sender says what its tenant is called with ego.tenant.name; ego keeps it unless a person already named the
// tenant, and an anonymous push names nobody's tenant.
func TestSenderNamesItsTenant(t *testing.T) {
	store := NewSessionStore(nil)
	names, err := NewTenantNames(nil)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTenantNamer(names.Fill)

	store.IngestFor("core", namedTenantBody("a", "aaaa", "s1", "ws-1", "Billing"))
	if got := names.Name("core/ws-1"); got != "Billing" {
		t.Errorf("name = %q, want the sender's", got)
	}
	if err := names.Set("core/ws-2", "By hand"); err != nil {
		t.Fatal(err)
	}
	store.IngestFor("core", namedTenantBody("b", "bbbb", "s2", "ws-2", "From sender"))
	if got := names.Name("core/ws-2"); got != "By hand" {
		t.Errorf("name = %q, a name set by hand must win", got)
	}
	store.IngestFor("core", namedTenantBody("c", "cccc", "s3", "ws-3", "bad\x00name"))
	if got := names.Name("core/ws-3"); got != "" {
		t.Errorf("name = %q, an unacceptable name is ignored", got)
	}
	store.Ingest(namedTenantBody("d", "dddd", "s4", "ws-4", "Anon"))
	if got := names.Name("ws-4"); got != "" {
		t.Errorf("name = %q, an anonymous push names no tenant", got)
	}
}
