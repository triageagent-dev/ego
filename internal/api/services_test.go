package api

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// servicedBody is a one-span export from service svc, of tenant label, under session name.
func servicedBody(session, traceID, spanID, label, svc string) map[string]any {
	body := labelledTracesBody(session, traceID, spanID, label)
	res := body["resourceSpans"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	res["attributes"] = append(res["attributes"].([]any), map[string]any{"key": "service.name", "value": map[string]any{"stringValue": svc}})
	return body
}

// The services that reported into a tenant are counted per tenant, offered with the tenant, and filter the session
// list, in memory and in the archive. They narrow what a role already sees: a tenant the role lacks lists none.
func TestServicesOfATenantAreCountedAndFilterTheList(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	store.Ingest(servicedBody("a", "t1", "s1", "ws", "core"))
	store.Ingest(servicedBody("b", "t2", "s2", "ws", "core"))
	store.Ingest(servicedBody("c", "t3", "s3", "ws", "helper"))
	store.Ingest(servicedBody("d", "t4", "s4", "other", "core"))
	for _, id := range []string{"a", "b", "c", "d"} {
		store.completeSession(id)
	}
	if err := store.flushPersist(); err != nil {
		t.Fatal(err)
	}

	names, _ := NewTenantNames(nil)
	opts, err := tenantOptions(store, names, RoleInfo{Role: RoleAdmin, IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]tenantOption{}
	for _, o := range opts {
		byKey[o.Key] = o
	}
	if got := byKey["ws"].Services; len(got) != 2 || got[0] != (serviceOption{"core", 2}) || got[1] != (serviceOption{"helper", 1}) {
		t.Errorf("services of ws = %+v, want core 2 then helper 1", got)
	}
	if got := byKey["other"].Services; len(got) != 1 || got[0].Name != "core" {
		t.Errorf("services of other = %+v", got)
	}

	// A role limited to "other" is offered no service of ws.
	limited, _ := tenantOptions(store, names, RoleInfo{Role: RoleMember, Tenants: []string{"other"}})
	if len(limited) != 1 || limited[0].Key != "other" {
		t.Errorf("a limited role is offered %+v, want only other", limited)
	}

	all := func(SessionSummary) bool { return true }
	for _, tc := range []struct {
		name string
		f    mcptools.SessionFilter
		want []string
	}{
		{"tenant and service", mcptools.SessionFilter{Tenant: "ws", Service: "helper"}, []string{"c"}},
		{"service alone", mcptools.SessionFilter{Service: "core"}, []string{"a", "b", "d"}},
		{"no such service", mcptools.SessionFilter{Service: "nope"}, nil},
	} {
		page, err := store.Page(SessionPageRequest{Filter: tc.f, Unrestricted: true}, all)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, it := range page.Items {
			got = append(got, it.SessionID)
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: sessions = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A session indexed before services existed has none until it is read back.
	if _, err := archive.db.Exec(`UPDATE session_index SET services = NULL WHERE session_id = 'a'`); err != nil {
		t.Fatal(err)
	}
	if n, err := archive.BackfillSessionIndex(DefaultTenantAttr); err != nil || n != 1 {
		t.Fatalf("backfill = %d, %v, want the one row without services", n, err)
	}
	var svc string
	archive.db.QueryRow(`SELECT services FROM session_index WHERE session_id = 'a'`).Scan(&svc)
	if svc != indexSep+"core"+indexSep {
		t.Errorf("backfilled services = %q", svc)
	}
}
