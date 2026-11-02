package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The live feed receives only control frames, so a data frame past the limit ends the connection instead of being
// buffered whole.
func TestWSUpdatesRefusesAnOversizeFrame(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/ui-updates", wsUpdatesHandler(newSSEHub()))
	server := httptest.NewServer(mux)
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/ui-updates", nil)
	if err != nil {
		t.Fatalf("dialing WS: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 4*wsReadLimit))); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("the server kept the connection after an oversize frame")
	}
}

func TestRoleBindingScopeIsValidated(t *testing.T) {
	roles, err := NewRoleStore(nil, RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := RoleBinding{SubjectType: "user", Subject: "alice", Role: RoleViewer}
	for name, mutate := range map[string]func(*RoleBinding){
		"empty tenants":       func(b *RoleBinding) { b.Tenants = []string{} },
		"empty tenant entry":  func(b *RoleBinding) { b.Tenants = []string{""} },
		"tenant with a space": func(b *RoleBinding) { b.Tenants = []string{"acme prod"} },
		"tenant, empty label": func(b *RoleBinding) { b.Tenants = []string{"core/"} },
		"empty agents":        func(b *RoleBinding) { b.Agents = []string{} },
		"blank agent":         func(b *RoleBinding) { b.Agents = []string{" "} },
	} {
		b := base
		mutate(&b)
		if _, err := roles.Upsert(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, tenants := range map[string][]string{"absent": nil, "star": {"*"}, "key": {"core/acme"}, "sender": {"core/*"}, "bare": {"acme"}} {
		b := base
		b.Tenants = tenants
		if _, err := roles.Upsert(b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A misspelt key on an admin body must not become a binding with no limit.
func TestRoleEndpointRejectsUnknownFields(t *testing.T) {
	roles, err := NewRoleStore(nil, RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := adminRolesHandler(roles)
	admin := RoleInfo{Role: RoleAdmin, IsAdmin: true}
	post := func(body string) int {
		req := withRoleInfoForTest(admin)(httptest.NewRequest(http.MethodPost, "/api/admin/roles", strings.NewReader(body)))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	if code := post(`{"subjectType":"user","subject":"alice","role":"viewer","tenant":"core/acme"}`); code != http.StatusBadRequest {
		t.Errorf("singular tenant key = %d, want 400", code)
	}
	if code := post(`{"subjectType":"user","subject":"alice","role":"viewer","tenants":[]}`); code != http.StatusBadRequest {
		t.Errorf("empty tenants = %d, want 400", code)
	}
	if code := post(`{"subjectType":"user","subject":"alice","role":"viewer","tenants":["core/acme"]}`); code != http.StatusOK {
		t.Errorf("valid binding = %d, want 200", code)
	}
	if n := len(roles.List()); n != 1 {
		t.Errorf("bindings stored = %d, want 1", n)
	}
}

func TestMergeEndpointValidatesBothKeys(t *testing.T) {
	senders, err := NewSenderStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := senders.Create("core", ""); err != nil {
		t.Fatal(err)
	}
	names, _ := NewTenantNames(nil)
	h := adminMergeTenantsHandler(NewSessionStore(nil), names, senders, nil)
	admin := RoleInfo{Role: RoleAdmin, IsAdmin: true}
	post := func(body string) int {
		req := withRoleInfoForTest(admin)(httptest.NewRequest(http.MethodPost, "/api/admin/merge-tenants", strings.NewReader(body)))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	for _, bad := range []string{
		`{"from":"ws1","to":"nosuch/ws1"}`,    // a sender that does not exist
		`{"from":"ws1","to":"co:re/ws1"}`,     // not a sender id
		`{"from":"a b","to":"core/ws1"}`,      // from is a key too
		`{"from":"Core/ws1","to":"core/ws1"}`, // from names a sender that cannot exist
	} {
		if code := post(bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	if code := post(`{"from":"ws1","to":"core/ws1","dryRun":true}`); code != http.StatusOK {
		t.Errorf("valid dry run = %d, want 200", code)
	}
}

// Ledger rows still waiting for the next flush follow the merge, or they would be written under the old key.
func TestMergeRekeysPendingLedgerRows(t *testing.T) {
	s := NewSessionStore(nil)
	s.pendingUsage = []usageRow{{SpanID: "a", Tenant: "ws1"}, {SpanID: "b", Tenant: "ws2"}}
	s.rekeyPendingUsage("ws1", "core/ws1")
	if s.pendingUsage[0].Tenant != "core/ws1" || s.pendingUsage[1].Tenant != "ws2" {
		t.Errorf("pending rows = %+v", s.pendingUsage)
	}
}

// The unauthenticated health answer carries the status, never the failed write's path or database message.
func TestHealthHidesTheArchiveError(t *testing.T) {
	w, _, _ := testWatch(5<<30, 10<<30)
	w.writeFailed(errors.New("write /data/archive.db: disk I/O error"))
	h := w.health()
	if h["status"] != "failing" || h["failingSince"] == nil {
		t.Fatalf("health = %v", h)
	}
	if _, ok := h["error"]; ok {
		t.Errorf("health carries the error text: %v", h)
	}
}

// The notice is posted after the lock is released: a slow sink must not stall /api/health.
func TestStorageWatchNoticeDoesNotHoldTheLock(t *testing.T) {
	w, _, _ := testWatch(5<<30, 10<<30)
	healthDone := make(chan struct{})
	w.notice = func(string) {
		go func() { w.health(); close(healthDone) }()
		select {
		case <-healthDone:
		case <-time.After(2 * time.Second):
			t.Error("/api/health waited for the notice to finish")
		}
	}
	w.writeFailed(errors.New("disk full"))
}

// A bootstrap admin who is demoted or removed stays so after a restart, although the list still names them.
func TestBootstrapAdminStaysDemotedAcrossRestarts(t *testing.T) {
	path := t.TempDir() + "/roles.db"
	open := func() (*RoleStore, *SQLiteStore) {
		t.Helper()
		archive, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		roles, err := NewRoleStore(archive, RoleNone, []string{"alice", "bob"})
		if err != nil {
			t.Fatal(err)
		}
		return roles, archive
	}
	roles, archive := open()
	if n := len(roles.List()); n != 2 {
		t.Fatalf("bootstrap grants = %d, want 2", n)
	}
	for _, b := range roles.List() {
		switch b.Subject {
		case "alice": // removed outright
			if ok, err := roles.Delete(b.ID); !ok || err != nil {
				t.Fatalf("delete: %v %v", ok, err)
			}
		case "bob": // demoted
			b.Role = RoleViewer
			if _, err := roles.Upsert(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	archive.Close()

	roles, archive = open()
	defer archive.Close()
	for _, b := range roles.List() {
		if b.Role == RoleAdmin {
			t.Errorf("%s is admin again after a restart", b.Subject)
		}
	}
	if n := len(roles.List()); n != 1 {
		t.Errorf("bindings after restart = %d, want only bob as a viewer", n)
	}
}

// An existing deployment has admin bindings but no record: they are recorded, not granted twice.
func TestBootstrapRecordsExistingBindings(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/roles.db")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	roles, err := NewRoleStore(archive, RoleNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := roles.Upsert(RoleBinding{SubjectType: "user", Subject: "carol", Role: RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	roles, err = NewRoleStore(archive, RoleNone, []string{"carol"})
	if err != nil {
		t.Fatal(err)
	}
	got := roles.List()
	if len(got) != 1 || got[0].ID != b.ID || got[0].Role != RoleViewer {
		t.Errorf("bindings = %+v, want carol's viewer binding untouched", got)
	}
}

// A role change or a revocation ends the open streams, which then reconnect under the new role.
func TestStreamsEndWhenARoleChanges(t *testing.T) {
	hub := newSSEHub()
	roles, err := NewRoleStore(nil, RoleNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	roles.SetOnChange(hub.kickAll)
	revoked, err := newRevocations(nil)
	if err != nil {
		t.Fatal(err)
	}
	revoked.SetOnChange(hub.kickAll)

	ended := func(ch chan any) bool {
		select {
		case _, ok := <-ch:
			return !ok
		case <-time.After(time.Second):
			return false
		}
	}
	ch := hub.register()
	if _, err := roles.Upsert(RoleBinding{SubjectType: "user", Subject: "dave", Role: RoleViewer}); err != nil {
		t.Fatal(err)
	}
	if !ended(ch) {
		t.Fatal("a stream survived a role binding change")
	}
	hub.unregister(ch) // the handler's deferred unregister after a kick must not panic

	ch = hub.register()
	if err := revoked.RevokeSubject("dave", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !ended(ch) {
		t.Fatal("a stream survived a revocation")
	}
	hub.broadcast(map[string]string{"type": "x"}) // no send on a closed channel
}
