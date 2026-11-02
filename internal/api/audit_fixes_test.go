package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/triageagent-dev/ego/internal/mcptools"
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

// A token that only sends traces is not a read credential: /mcp accepts it only when it was minted with mcp.
func TestIngestTokenReadsOverMCPOnlyWhenMintedWithIt(t *testing.T) {
	f := newMCPFixture(t)
	senders, err := NewSenderStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := senders.Create("core", ""); err != nil {
		t.Fatal(err)
	}
	sendOnly, meta, _ := senders.NewToken("core", "", "", false)
	if meta.MCP {
		t.Fatal("a token minted without mcp reports mcp")
	}
	reader, _, _ := senders.NewToken("core", "", "", true)
	roles, err := NewRoleStore(nil, RoleNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := mcpRoute("a-session-secret-that-is-long-enough", senders, roles, "", nil, nil, f.backend)

	if code, _ := callTool(t, h, sendOnly, "get_quality", nil); code != http.StatusUnauthorized && code != http.StatusFound {
		t.Errorf("send-only token on /mcp = %d, want it refused", code)
	}
	if code, res := callTool(t, h, reader, "get_quality", nil); code != http.StatusOK || res.IsError {
		t.Errorf("token minted with mcp = %d %+v", code, res)
	}
}

// A token stored before the column existed keeps the read access it had.
func TestStoredSenderTokensKeepMCPAccess(t *testing.T) {
	path := t.TempDir() + "/s.db"
	archive, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	senders, err := NewSenderStore(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := senders.Create("core", ""); err != nil {
		t.Fatal(err)
	}
	// A row as the previous release wrote it: no mcp column value.
	if _, err := archive.db.Exec(`INSERT INTO sender_tokens (id, sender, label, hash, created_at, tenant) VALUES ('st-old', 'core', '', ?, '2026-01-01T00:00:00Z', '')`, hashSenderToken("egs_old")); err != nil {
		t.Fatal(err)
	}
	archive.Close()
	archive, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	senders, err = NewSenderStore(archive)
	if err != nil {
		t.Fatal(err)
	}
	tok, ok := senders.Resolve("egs_old")
	if !ok || !tok.MCP {
		t.Errorf("a token stored before the column = %+v (found %v), want mcp true", tok, ok)
	}
}

// One identity keeps a few streams; a further one replaces its oldest, and another identity is not touched.
func TestStreamsPerIdentityAreCappedByEvictingTheOldest(t *testing.T) {
	hub := newSSEHub()
	info := RoleInfo{Role: RoleViewer}
	var mine []chan any
	for i := 0; i < maxStreamsPerIdentity; i++ {
		mine = append(mine, hub.registerWho(info, "github:alice"))
	}
	other := hub.registerWho(info, "github:bob")
	extra := hub.registerWho(info, "github:alice")
	if extra == nil || other == nil {
		t.Fatal("a stream was refused below the global cap")
	}
	if _, ok := <-mine[0]; ok {
		t.Error("the oldest stream of the identity is still open")
	}
	for i, ch := range mine[1:] {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Errorf("stream %d of the identity was closed", i+1)
			}
		default:
		}
	}
	select {
	case _, ok := <-other:
		if !ok {
			t.Error("another identity lost a stream")
		}
	default:
	}
	hub.unregister(mine[0]) // the handler's deferred unregister after an eviction must not panic
}

func TestHubRefusesStreamsPastTheGlobalCap(t *testing.T) {
	hub := newSSEHub()
	for i := 0; i < maxStreams; i++ {
		if hub.registerWho(RoleInfo{Role: RoleViewer}, "") == nil {
			t.Fatalf("stream %d refused", i)
		}
	}
	if hub.registerWho(RoleInfo{Role: RoleViewer}, "") != nil {
		t.Error("a stream past the cap was accepted")
	}
}

// A viewer limited to one tenant: the search reads that tenant's rows only, and the paged list scans only them.
func TestSearchAndPageReadOnlyTheCallersTenantsInSQL(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	first := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	for i, ws := range []string{"ws1", "ws2", "ws2", "ws2"} {
		id := "s" + string(rune('a'+i))
		first.Ingest(labelledTracesBody(id, "t"+id, "needle-"+id, ws))
		first.completeSession(id)
	}
	if err := first.flushPersist(); err != nil {
		t.Fatal(err)
	}

	var seen []string
	err = archive.SearchSnapshots("needle", time.Time{}, 100, []string{"ws1"}, func(snap sessionSnapshot) bool {
		seen = append(seen, snap.SessionID)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "sa" {
		t.Errorf("search for a ws1 viewer parsed %v, want only sa", seen)
	}
	seen = nil
	_ = archive.SearchSnapshots("needle", time.Time{}, 100, nil, func(snap sessionSnapshot) bool { seen = append(seen, snap.SessionID); return true })
	if len(seen) != 4 {
		t.Errorf("an unrestricted search parsed %d sessions, want 4", len(seen))
	}
	seen = nil
	_ = archive.SearchSnapshots("needle", time.Time{}, 100, []string{}, func(snap sessionSnapshot) bool { seen = append(seen, snap.SessionID); return true })
	if len(seen) != 0 {
		t.Errorf("a role with an empty tenant limit parsed %v, want nothing", seen)
	}

	rows, err := archive.indexPage(sessionPageFilter{roleTenants: []string{"ws1"}}, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "sa" {
		t.Errorf("index page for a ws1 viewer = %d rows, want only sa", len(rows))
	}
	rows, _ = archive.indexPage(sessionPageFilter{roleTenants: []string{"ws*"}}, nil, 100)
	if len(rows) != 4 {
		t.Errorf("a pattern SQL cannot express must not narrow: %d rows, want 4", len(rows))
	}
}

// Text searches run a few at a time; the others are told to retry.
func TestArchiveSearchesAreGated(t *testing.T) {
	archive, err := NewSQLiteStore(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	store := NewSessionStoreWithArchive(nil, archive, SessionLimits{})
	for i := 0; i < readConcurrency; i++ {
		if !store.searchReads.acquire() {
			t.Fatal("a place was taken too early")
		}
	}
	if _, busy := store.FindFor(mcptoolsFilter("needle"), RoleInfo{Role: RoleViewer}); !busy {
		t.Error("a search with every place taken was not told to retry")
	}
	if _, busy := store.FindFor(mcptoolsFilter(""), RoleInfo{Role: RoleViewer}); busy {
		t.Error("a listing without a query was gated")
	}
}

func mcptoolsFilter(q string) mcptools.SessionFilter { return mcptools.SessionFilter{Query: q} }
