package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/triageagent-dev/ego/internal/otlp"
)

func TestRoleStore_DefaultRoleWhenNoBindingMatches(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	info := s.EffectiveRole("nobody-configured", nil)
	if info.Role != RoleMember || info.Agents != nil || info.IsAdmin {
		t.Fatalf("EffectiveRole() = %+v, want unrestricted RoleMember", info)
	}
}

func TestRoleStore_BootstrapAdminIsIdempotent(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, []string{"octocat", "octocat", ""})
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	bindings := s.List()
	if len(bindings) != 1 {
		t.Fatalf("List() = %d bindings, want exactly 1 (deduplicated, blank skipped): %+v", len(bindings), bindings)
	}
	if bindings[0].Role != RoleAdmin || bindings[0].Subject != "octocat" {
		t.Fatalf("bootstrap binding = %+v, want admin binding for octocat", bindings[0])
	}

	info := s.EffectiveRole("octocat", nil)
	if !info.IsAdmin {
		t.Fatalf("EffectiveRole(octocat) = %+v, want IsAdmin", info)
	}

	// A second NewRoleStore over the same (in this test, still in-memory,
	// but the same principle applies across a real restart via archive)
	// bootstrap list must not add a duplicate binding.
	s2, err := NewRoleStore(nil, RoleMember, []string{"octocat"})
	if err != nil {
		t.Fatalf("NewRoleStore (second): %v", err)
	}
	if len(s2.List()) != 1 {
		t.Fatalf("second NewRoleStore() produced %d bindings, want 1", len(s2.List()))
	}
}

func TestRoleStore_DirectUserBindingOutranksTeamBinding(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	if _, err := s.Upsert(RoleBinding{SubjectType: "team", Subject: "acme/eng", Role: RoleViewer}); err != nil {
		t.Fatalf("Upsert(team): %v", err)
	}
	if _, err := s.Upsert(RoleBinding{SubjectType: "user", Subject: "alice", Role: RoleAdmin}); err != nil {
		t.Fatalf("Upsert(user): %v", err)
	}

	info := s.EffectiveRole("alice", []string{"acme/eng"})
	if !info.IsAdmin {
		t.Fatalf("EffectiveRole() = %+v, want IsAdmin (direct admin binding must outrank the viewer team binding)", info)
	}

	// A teammate with no binding of their own is scoped by the team
	// binding alone.
	info = s.EffectiveRole("teammate", []string{"acme/eng"})
	if info.Role != RoleViewer || info.IsAdmin {
		t.Fatalf("EffectiveRole(teammate) = %+v, want RoleViewer via the team binding", info)
	}
}

func TestRoleStore_AgentScopeUnionAtWinningRank(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	if _, err := s.Upsert(RoleBinding{SubjectType: "user", Subject: "viewer1", Role: RoleViewer, Agents: []string{"billing-agent"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := s.Upsert(RoleBinding{SubjectType: "team", Subject: "acme/support", Role: RoleViewer, Agents: []string{"support-agent"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	info := s.EffectiveRole("viewer1", []string{"acme/support"})
	if info.Role != RoleViewer {
		t.Fatalf("EffectiveRole().Role = %q, want viewer", info.Role)
	}
	if !info.AllowsAgent("billing-agent") || !info.AllowsAgent("support-agent") {
		t.Fatalf("EffectiveRole().Agents = %v, want both billing-agent and support-agent allowed", info.Agents)
	}
	if info.AllowsAgent("unrelated-agent") {
		t.Fatalf("EffectiveRole() unexpectedly allows an agent no matching binding scoped to it")
	}

	// One more binding at the same viewer rank with an *unrestricted*
	// scope (empty Agents) makes the union unrestricted, not just larger.
	if _, err := s.Upsert(RoleBinding{SubjectType: "user", Subject: "viewer1", ID: bindingIDFor(t, s, "viewer1"), Role: RoleViewer}); err != nil {
		t.Fatalf("Upsert (widen to unrestricted): %v", err)
	}
	info = s.EffectiveRole("viewer1", []string{"acme/support"})
	if info.Agents != nil {
		t.Fatalf("EffectiveRole().Agents = %v after widening one binding to unrestricted, want nil (unrestricted)", info.Agents)
	}
}

// bindingIDFor returns the ID of subject's existing binding, so a test can
// exercise Upsert's update-in-place path deliberately rather than by
// accident.
func bindingIDFor(t *testing.T, s *RoleStore, subject string) string {
	t.Helper()
	for _, b := range s.List() {
		if b.Subject == subject {
			return b.ID
		}
	}
	t.Fatalf("no existing binding for subject %q", subject)
	return ""
}

func TestRoleStore_CanWriteAndOpenDefault(t *testing.T) {
	admin := RoleInfo{Role: RoleAdmin, IsAdmin: true}
	member := RoleInfo{Role: RoleMember}
	viewer := RoleInfo{Role: RoleViewer}
	if !admin.CanWrite() || !member.CanWrite() {
		t.Fatalf("admin/member must be able to write")
	}
	if viewer.CanWrite() {
		t.Fatalf("viewer must not be able to write")
	}

	// roleInfoOrOpen with no RoleInfo in context (RBAC not configured at
	// all) must be a fully-open admin - never mistaken for "no access".
	open := roleInfoOrOpen(t.Context())
	if !open.IsAdmin || !open.CanWrite() {
		t.Fatalf("roleInfoOrOpen(no info) = %+v, want an open admin default", open)
	}
}

func TestRoleStore_UpsertValidation(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	cases := []RoleBinding{
		{SubjectType: "bogus", Subject: "x", Role: RoleAdmin},
		{SubjectType: "user", Subject: "", Role: RoleAdmin},
		{SubjectType: "team", Subject: "no-slash", Role: RoleAdmin},
		{SubjectType: "user", Subject: "x", Role: "superuser"},
	}
	for _, c := range cases {
		if _, err := s.Upsert(c); err == nil {
			t.Errorf("Upsert(%+v) succeeded, want a validation error", c)
		}
	}
}

func TestRoleStore_PersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "roles.db")

	archive1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("opening archive: %v", err)
	}
	s1, err := NewRoleStore(archive1, RoleMember, []string{"admin-user"})
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	if _, err := s1.Upsert(RoleBinding{SubjectType: "team", Subject: "acme/eng", Role: RoleViewer, Agents: []string{"a", "b"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := archive1.db.Close(); err != nil {
		t.Fatalf("closing archive1: %v", err)
	}

	archive2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopening archive: %v", err)
	}
	defer archive2.db.Close()
	// No bootstrap admins this time - proves the admin binding survived
	// the restart on its own, not because it was re-bootstrapped.
	s2, err := NewRoleStore(archive2, RoleMember, nil)
	if err != nil {
		t.Fatalf("NewRoleStore (restart): %v", err)
	}
	if len(s2.List()) != 2 {
		t.Fatalf("List() after restart = %d bindings, want 2: %+v", len(s2.List()), s2.List())
	}
	info := s2.EffectiveRole("admin-user", nil)
	if !info.IsAdmin {
		t.Fatalf("admin-user role after restart = %+v, want IsAdmin", info)
	}
	info = s2.EffectiveRole("someone-else", []string{"acme/eng"})
	if info.Role != RoleViewer || !info.AllowsAgent("a") || info.AllowsAgent("c") {
		t.Fatalf("team-bound viewer after restart = %+v, want viewer scoped to [a b]", info)
	}
}

func TestRoleStore_DeleteRemovesBinding(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	b, err := s.Upsert(RoleBinding{SubjectType: "user", Subject: "temp-admin", Role: RoleAdmin})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if ok, err := s.Delete(b.ID); err != nil || !ok {
		t.Fatalf("Delete(%q) = (%v, %v), want (true, nil)", b.ID, ok, err)
	}
	if ok, _ := s.Delete(b.ID); ok {
		t.Fatalf("Delete of an already-deleted ID reported ok=true")
	}
	if info := s.EffectiveRole("temp-admin", nil); info.IsAdmin {
		t.Fatalf("EffectiveRole() after Delete = %+v, want fallback to default role", info)
	}
}

// ---------------------------------------------------------------------------
// /api/admin/roles HTTP handlers
// ---------------------------------------------------------------------------

func withRoleInfoForTest(info RoleInfo) func(*http.Request) *http.Request {
	return func(r *http.Request) *http.Request {
		return r.WithContext(withRoleInfo(r.Context(), info))
	}
}

func TestAdminRolesHandler_RequiresAdmin(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	handler := adminRolesHandler(s)

	withCtx := withRoleInfoForTest(RoleInfo{Role: RoleMember})
	req := withCtx(httptest.NewRequest(http.MethodGet, "/api/admin/roles", nil))
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member GET /api/admin/roles = %d, want 403", rec.Code)
	}
}

func TestAdminRolesHandler_CreateListDelete(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatalf("NewRoleStore: %v", err)
	}
	handler := adminRolesHandler(s)
	byIDHandler := adminRoleByIDHandler(s)
	asAdmin := withRoleInfoForTest(RoleInfo{Role: RoleAdmin, IsAdmin: true})

	body, _ := json.Marshal(roleBindingRequest{SubjectType: "user", Subject: "new-viewer", Role: "viewer"})
	req := asAdmin(httptest.NewRequest(http.MethodPost, "/api/admin/roles", bytes.NewReader(body)))
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/admin/roles = %d, body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data RoleBinding `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if created.Data.ID == "" || created.Data.Role != RoleViewer {
		t.Fatalf("created binding = %+v, want a populated viewer binding", created.Data)
	}

	req = asAdmin(httptest.NewRequest(http.MethodGet, "/api/admin/roles", nil))
	rec = httptest.NewRecorder()
	handler(rec, req)
	var listed struct {
		Data []RoleBinding `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding list response: %v", err)
	}
	if len(listed.Data) != 1 {
		t.Fatalf("GET /api/admin/roles listed %d bindings, want 1", len(listed.Data))
	}

	req = asAdmin(httptest.NewRequest(http.MethodDelete, "/api/admin/roles/"+created.Data.ID, nil))
	req.SetPathValue("id", created.Data.ID)
	rec = httptest.NewRecorder()
	byIDHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/admin/roles/{id} = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(s.List()) != 0 {
		t.Fatalf("List() after delete = %d, want 0", len(s.List()))
	}
}

func TestAdminRolesHandler_NilStoreUnavailable(t *testing.T) {
	handler := adminRolesHandler(nil)
	asAdmin := withRoleInfoForTest(RoleInfo{Role: RoleAdmin, IsAdmin: true})
	req := asAdmin(httptest.NewRequest(http.MethodGet, "/api/admin/roles", nil))
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET with nil roleStore = %d, want 503", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Viewer write-gating on mutating handlers
// ---------------------------------------------------------------------------

func TestEvaluateHandler_ViewerCannotWrite(t *testing.T) {
	tr := buildTrajectoryTrace()
	content, err := otlp.EncodeTraceJSONL(tr)
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}
	config := `{"evaluators":[{"type":"builtin","name":"tool_trajectory_avg_score"}]}`
	req := withRoleInfoForTest(RoleInfo{Role: RoleViewer})(multipartEvaluateRequest(t, content, nil, config))
	rec := httptest.NewRecorder()

	evaluateHandler(nil)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/evaluate = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvaluateHandler_MemberCanWrite(t *testing.T) {
	tr := buildTrajectoryTrace()
	content, err := otlp.EncodeTraceJSONL(tr)
	if err != nil {
		t.Fatalf("EncodeTraceJSONL: %v", err)
	}
	config := `{"evaluators":[{"type":"builtin","name":"tool_trajectory_avg_score"}]}`
	req := withRoleInfoForTest(RoleInfo{Role: RoleMember})(multipartEvaluateRequest(t, content, nil, config))
	rec := httptest.NewRecorder()

	evaluateHandler(nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("member POST /api/evaluate = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

func TestSaveEvalSetHandler_ViewerCannotWrite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "evalsets.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer store.db.Close()

	body, _ := json.Marshal(map[string]any{
		"eval_set_id": "set-1",
		"eval_cases":  []map[string]any{{"eval_id": "case-1", "conversation": []map[string]any{}}},
	})
	req := withRoleInfoForTest(RoleInfo{Role: RoleViewer})(httptest.NewRequest(http.MethodPost, "/api/evalsets", bytes.NewReader(body)))
	rec := httptest.NewRecorder()

	saveEvalSetHandler(store)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/evalsets = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Per-agent scoping on /api/runs*
// ---------------------------------------------------------------------------

func TestRunVisibleForRole(t *testing.T) {
	unrestricted := RoleInfo{Role: RoleMember}
	scoped := RoleInfo{Role: RoleViewer, Agents: []string{"allowed-agent"}}

	runNoSummary := runDTO{RunID: "r1"}
	runKnownAllowed := runDTO{RunID: "r2", Summary: &runSummaryDTO{Agents: []string{"allowed-agent"}}}
	runKnownDisallowed := runDTO{RunID: "r3", Summary: &runSummaryDTO{Agents: []string{"other-agent"}}}
	runUnknownAgents := runDTO{RunID: "r4", Summary: &runSummaryDTO{}}

	for _, run := range []runDTO{runNoSummary, runKnownAllowed, runKnownDisallowed, runUnknownAgents} {
		if !runVisibleForRole(run, unrestricted) {
			t.Errorf("run %s hidden from an unrestricted role", run.RunID)
		}
	}
	if !runVisibleForRole(runNoSummary, scoped) {
		t.Error("a run with no summary yet must stay visible even to a scoped role")
	}
	if !runVisibleForRole(runKnownAllowed, scoped) {
		t.Error("a run whose agent is in the scoped allowlist must be visible")
	}
	if runVisibleForRole(runKnownDisallowed, scoped) {
		t.Error("a run whose only known agent is outside the scoped allowlist must be hidden")
	}
	if !runVisibleForRole(runUnknownAgents, scoped) {
		t.Error("a run with an empty/unknown Agents list must stay visible (can't tell, so allow)")
	}
}

func TestDistinctAgentNames(t *testing.T) {
	names := distinctAgentNames([]traceResultDTO{
		{AgentName: "b"}, {AgentName: "a"}, {AgentName: ""}, {AgentName: "a"},
	})
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("distinctAgentNames() = %v, want [a b]", names)
	}
	if got := distinctAgentNames([]traceResultDTO{{AgentName: ""}}); got != nil {
		t.Fatalf("distinctAgentNames() with no known agents = %v, want nil", got)
	}
}
