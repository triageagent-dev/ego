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
