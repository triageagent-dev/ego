package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// agentTracesBody is one root span of the named agent in the named session.
func agentTracesBody(session, traceID, spanID, agent string) map[string]any {
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{
			map[string]any{"key": sessionNameAttr, "value": map[string]any{"stringValue": session}},
		}},
		"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId": traceID, "spanId": spanID, "name": "agent.run",
			"startTimeUnixNano": "1000000000", "endTimeUnixNano": "1010000000",
			"attributes": []any{map[string]any{"key": "gen_ai.agent.name", "value": map[string]any{"stringValue": agent}}},
		}}}},
	}}}
}

func scopedTo(agents ...string) RoleInfo {
	return RoleInfo{Role: RoleViewer, Agents: agents}
}

func TestScopedRoleNeverSeesUnknownAgent(t *testing.T) {
	scoped := scopedTo("a")
	if scoped.AllowsAgent("") || scoped.AllowsAnyAgent(nil) || scoped.AllowsAnyAgent([]string{""}) {
		t.Fatal("a scoped role must not see a result whose agent is unknown")
	}
	if !scoped.AllowsAnyAgent([]string{"b", "a"}) || scoped.AllowsAnyAgent([]string{"b"}) {
		t.Fatal("a scoped role sees a result exactly when one of its agents is in scope")
	}
	open := RoleInfo{Role: RoleMember}
	if !open.AllowsAgent("") || !open.AllowsAnyAgent(nil) {
		t.Fatal("an unscoped role sees everything")
	}
}

func TestSessionEndpointsHideOutOfScopeSessions(t *testing.T) {
	store := NewSessionStore(nil)
	store.Ingest(agentTracesBody("mine", "aaaa", "s1", "a"))
	store.Ingest(agentTracesBody("theirs", "bbbb", "s2", "b"))
	store.Ingest(agentTracesBody("anonymous", "cccc", "s3", ""))
	as := withRoleInfoForTest(scopedTo("a"))

	rec := httptest.NewRecorder()
	sessionsHandler(store)(rec, as(httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil)))
	var listed struct {
		Data []SessionSummary `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0].SessionID != "mine" {
		t.Fatalf("scoped list = %+v, want only the in-scope session", listed.Data)
	}

	rec = httptest.NewRecorder()
	sessionsHandler(store)(rec, as(httptest.NewRequest(http.MethodGet, "/api/streaming/sessions?id=theirs", nil)))
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed.Data) != 0 {
		t.Fatalf("?id= of an out-of-scope session = %s, want an empty list", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	sessionSpansHandler(store)(rec, as(httptest.NewRequest(http.MethodGet, "/api/streaming/session-spans?session_id=theirs", nil)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("session-spans of an out-of-scope session = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	sessionSpansHandler(store)(rec, as(httptest.NewRequest(http.MethodGet, "/api/streaming/session-spans?session_id=mine", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("session-spans of an in-scope session = %d, want 200", rec.Code)
	}

	for name, h := range map[string]http.HandlerFunc{"get-trace": getTraceHandler(store), "create-eval-set": createEvalSetHandler(store)} {
		rec = httptest.NewRecorder()
		h(rec, as(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"session_id":"theirs"}`))))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s of an out-of-scope session = %d, want 404", name, rec.Code)
		}
	}

	rec = httptest.NewRecorder()
	sessionsHandler(store)(rec, httptest.NewRequest(http.MethodGet, "/api/streaming/sessions", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed.Data) != 3 {
		t.Fatalf("unscoped list = %s, want all three sessions", rec.Body.String())
	}
}

func TestUnattributableViewsAreDeniedToScopedRoles(t *testing.T) {
	as := withRoleInfoForTest(scopedTo("a"))
	for name, h := range map[string]http.HandlerFunc{
		"usage":        usageHandler(nil),
		"usage calls":  usageCallsHandler(nil),
		"eval sets":    listEvalSetsHandler(nil),
		"eval set":     getEvalSetHandler(nil),
		"online state": onlineStatusHandler(nil),
		"online hours": onlineSeriesHandler(nil),
		"online alert": onlineAlertsHandler(nil),
	} {
		rec := httptest.NewRecorder()
		h(rec, as(httptest.NewRequest(http.MethodGet, "/", nil)))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s for a scoped role = %d, want 403", name, rec.Code)
		}
		rec = httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s for an unscoped role = 403, want it served", name)
		}
	}
}

func TestHubScopedClientGetsOnlyItsAgents(t *testing.T) {
	hub := newSSEHub()
	scoped := hub.registerFor(scopedTo("a"))
	all := hub.register()
	defer hub.unregister(scoped)
	defer hub.unregister(all)

	hub.broadcastSession("started-b", "sb", []string{"b"}, nil, true)
	hub.broadcastSession("started-a", "sa", []string{"a"}, nil, true)
	hub.broadcastSession("update-a", "sa", []string{"a"}, nil, false)
	hub.broadcastSession("update-none", "sn", nil, nil, false)
	hub.broadcast("no-session")

	var got []any
	for len(scoped) > 0 {
		got = append(got, <-scoped)
	}
	if len(got) != 2 || got[0] != "started-a" || got[1] != "update-a" {
		t.Fatalf("scoped client received %v, want only the in-scope session's events", got)
	}
	if len(all) != 5 {
		t.Fatalf("unscoped client received %d events, want all 5", len(all))
	}
	if hub.takeResync(scoped) {
		t.Fatal("no resync is needed when every visible session was announced")
	}
}

// A session whose agent name arrives only in a later span was never announced to the scoped client; the first
// update that makes it visible must send the client to re-read the list rather than reach it unannounced.
func TestHubScopedClientResyncsForSessionThatBecomesVisible(t *testing.T) {
	hub := newSSEHub()
	scoped := hub.registerFor(scopedTo("a"))
	defer hub.unregister(scoped)

	hub.broadcastSession("started", "late", nil, nil, true)
	hub.broadcastSession("update", "late", []string{"a"}, nil, false)

	if len(scoped) != 0 {
		t.Fatalf("scoped client received %d events for an unannounced session, want none", len(scoped))
	}
	if !hub.takeResync(scoped) {
		t.Fatal("expected a resync once the session became visible")
	}
}

func TestEffectiveRoleRefusesWhenTeamsUnknown(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info := s.EffectiveRole("nobody", nil, false); info.Unresolved || info.Role != RoleMember {
		t.Fatalf("without team bindings the unknown teams do not matter, got %+v", info)
	}

	if _, err := s.Upsert(RoleBinding{SubjectType: "team", Subject: "acme/support", Role: RoleViewer, Agents: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(RoleBinding{SubjectType: "user", Subject: "alice", Role: RoleAdmin}); err != nil {
		t.Fatal(err)
	}

	info := s.EffectiveRole("bob", nil, false)
	if !info.Unresolved || !info.Scoped() || info.CanWrite() {
		t.Fatalf("unknown teams with a team binding = %+v, want Unresolved and nothing visible", info)
	}
	if info := s.EffectiveRole("bob", nil, true); info.Unresolved || info.Role != RoleMember {
		t.Fatalf("known team-less user = %+v, want the default role", info)
	}
	if info := s.EffectiveRole("alice", nil, false); info.Unresolved || !info.IsAdmin {
		t.Fatalf("a user bound by name keeps that role = %+v", info)
	}
}

func TestWithRoleAsksForNewSignInWhenTeamsAreGone(t *testing.T) {
	s, err := NewRoleStore(nil, RoleMember, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(RoleBinding{SubjectType: "team", Subject: "acme/support", Role: RoleViewer}); err != nil {
		t.Fatal(err)
	}
	called := false
	h := withRole(s, "acme", newGitHubTokenValidator(&GitHubOAuthConfig{Org: "acme"}), func(http.ResponseWriter, *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "x"})
	req = req.WithContext(withIdentity(req.Context(), identity{Username: "bob", Kind: tokenKindSession}))
	rec := httptest.NewRecorder()
	h(rec, req)
	if called || rec.Code != http.StatusUnauthorized {
		t.Fatalf("restarted pod, cookie session: handler called=%v status=%d, want 401 and no handler", called, rec.Code)
	}

	s.setTeams("bob", nil)
	rec = httptest.NewRecorder()
	h(rec, req)
	if !called {
		t.Fatalf("after the teams are known again the request must pass, got %d", rec.Code)
	}
}
