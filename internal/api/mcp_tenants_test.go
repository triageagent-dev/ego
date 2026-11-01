package api

import (
	"strings"
	"testing"
)

// list_tenants answers with the caller's own tenants, named as the switcher names them, and refuses an
// agent-scoped role, exactly like GET /api/tenants.
func TestMCPBackend_TenantsAreTheCallersOwn(t *testing.T) {
	f := newToolFixture(t)
	// The switcher reads tenants from the archive, which the store writes on its flush timer.
	if err := f.store.flushPersist(); err != nil {
		t.Fatal(err)
	}
	names, err := NewTenantNames(f.backend.archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := names.Set("core/acme", "Acme"); err != nil {
		t.Fatal(err)
	}
	f.backend.names = names

	core, err := f.backend.Tenants(asRole(RoleInfo{Role: RoleMember, Tenants: []string{"core/*"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(core) != 1 || core[0].Key != "core/acme" || core[0].Sender != "core" || core[0].Label != "acme" ||
		core[0].Name != "Acme" || core[0].Sessions != 2 {
		t.Errorf("core/* sees %+v, want core/acme named Acme with 2 sessions", core)
	}

	rc, err := f.backend.Tenants(asRole(RoleInfo{Role: RoleViewer, Tenants: []string{"rc/acme"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(rc) != 1 || rc[0].Key != "rc/acme" || rc[0].Name != "" || rc[0].Sessions != 1 {
		t.Errorf("rc/acme sees %+v, want rc/acme unnamed with 1 session", rc)
	}

	admin, err := f.backend.Tenants(asRole(RoleInfo{Role: RoleAdmin, IsAdmin: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(admin) != 2 || admin[0].Key != "core/acme" || admin[1].Key != "rc/acme" {
		t.Errorf("admin sees %+v, want both tenants sorted by key", admin)
	}

	if _, err := f.backend.Tenants(asRole(RoleInfo{Role: RoleMember, Agents: []string{"ops"}})); err == nil ||
		!strings.Contains(err.Error(), "agent-scoped") {
		t.Errorf("agent-scoped role: err = %v, want a refusal", err)
	}
}
