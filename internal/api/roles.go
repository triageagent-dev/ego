// roles.go implements multi-user role-based access control: GitHub OAuth (oauth.go) only ever gated access as a single
// all-or-nothing "active member of EGO_GITHUB_ORG" check with no
// notion of who's allowed to do what once inside. An admin now assigns a
// Role - and optionally a per-agent visibility scope - to a GitHub
// username or a GitHub team (org/slug), via the /api/admin/roles REST API
// (roleshandlers.go) or the matching MCP tools (cmd/ego/mcp.go).
package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Role is one of three access levels a RoleBinding grants. Ranked
// admin > member > viewer (see Role.rank): when several bindings match the
// same request (a user bound directly, plus one or more of their teams),
// the highest-ranked matching role wins outright - see RoleStore.
// EffectiveRole's doc comment for why bindings at a lower rank than the
// winner are otherwise ignored rather than blended in.
type Role string

const (
	RoleAdmin  Role = "admin"  // full read/write, plus manage role bindings themselves.
	RoleMember Role = "member" // full read/write, subject to any Agents scope - the behavior of a deployment with no roles configured.
	RoleViewer Role = "viewer" // read-only, subject to any Agents scope.
	// RoleNone only exists as a default role: whoever no binding covers gets no access at all, the way a subject with no
	// grant gets nothing in the core. It cannot be put in a binding.
	RoleNone Role = "none"
)

// ParseRole validates a role string from a REST request body or a
// --default-role/EGO_DEFAULT_ROLE flag/env value.
func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleAdmin, RoleMember, RoleViewer:
		return Role(s), nil
	default:
		return "", fmt.Errorf("invalid role %q (must be %q, %q, or %q)", s, RoleAdmin, RoleMember, RoleViewer)
	}
}

// ParseDefaultRole is ParseRole for --default-role, which also takes "none".
func ParseDefaultRole(s string) (Role, error) {
	if Role(s) == RoleNone {
		return RoleNone, nil
	}
	r, err := ParseRole(s)
	if err != nil {
		return "", fmt.Errorf("invalid default role %q (must be admin, member, viewer or none)", s)
	}
	return r, nil
}

func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleMember:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// RoleBinding assigns Role to one subject: a GitHub username (SubjectType "user", Subject its login) or a
// GitHub team (SubjectType "team", Subject "<org>/<team-slug>"). Agents, when non-empty, restricts what the
// subject can see to only those agent names (as reported by adk.TraceMetadata.AgentName - see
// runSummaryDTO.Agents/traceResultDTO. AgentName); empty/nil means unrestricted, every agent visible.
type RoleBinding struct {
	ID          string   `json:"id"`
	SubjectType string   `json:"subjectType"` // "user" | "team"
	Subject     string   `json:"subject"`     // GitHub login, or "org/team-slug"
	Role        Role     `json:"role"`
	Agents      []string `json:"agents,omitempty"`
	// Tenants, when non-empty, limits what the subject can see to data of those tenants (a sender'"'"'s tenant, see
	// tenants.go); empty means every tenant. Combined with Agents, both must allow.
	Tenants   []string `json:"tenants,omitempty"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// subjectKey is the lowercase "type:subject" form matched against a
// request's resolved username/teams in EffectiveRole.
func (b RoleBinding) subjectKey() string {
	return b.SubjectType + ":" + strings.ToLower(b.Subject)
}

// teamCacheTTL bounds how long a browser (cookie-authenticated) user's
// GitHub team memberships, resolved once at /auth/callback login time (the
// only point this service ever holds that user's own GitHub access token -
// see authCallbackHandler), are trusted before team-based bindings stop
// applying to their session. Matches sessionTTL: both are refreshed
// together at the user's next login, so this never outlives the session
// cookie it's paired with.
const teamCacheTTL = sessionTTL

type teamCacheEntry struct {
	teams   []string
	expires time.Time
}

// RoleStore holds every RoleBinding this deployment has configured, plus a
// short-lived per-username cache of GitHub team memberships (see
// teamCacheTTL). In-memory always; additionally durable across restarts
// when archive is non-nil (the same optional --session-db/
// EGO_SESSION_DB_PATH SQLite file sessions/runs/eval sets already
// use - see rolesstore.go).
type RoleStore struct {
	mu          sync.Mutex
	bindings    map[string]RoleBinding
	archive     *SQLiteStore
	defaultRole Role
	teamCache   map[string]teamCacheEntry
}

// NewRoleStore loads any persisted bindings from archive (nil if no
// --session-db was configured - bindings are then in-memory-only for this
// process's lifetime), then idempotently grants RoleAdmin to every login
// in bootstrapAdmins that doesn't already have an admin binding - the only
// way to get a first admin into an otherwise-empty RoleStore, since
// nothing else can grant RoleAdmin without already having it. Re-running
// with the same bootstrapAdmins list is always safe: it never touches an
// existing binding (admin-demoted-by-another-admin stays demoted, not
// silently re-granted on the next restart).
func NewRoleStore(archive *SQLiteStore, defaultRole Role, bootstrapAdmins []string) (*RoleStore, error) {
	s := &RoleStore{
		bindings:    map[string]RoleBinding{},
		archive:     archive,
		defaultRole: defaultRole,
		teamCache:   map[string]teamCacheEntry{},
	}
	if archive != nil {
		bindings, err := archive.ListRoleBindings()
		if err != nil {
			return nil, fmt.Errorf("loading role bindings: %w", err)
		}
		for _, b := range bindings {
			s.bindings[b.ID] = b
		}
	}
	for _, login := range bootstrapAdmins {
		login = strings.TrimSpace(login)
		if login == "" {
			continue
		}
		if s.hasAdminBindingForUser(login) {
			continue
		}
		if _, err := s.Upsert(RoleBinding{SubjectType: "user", Subject: login, Role: RoleAdmin}); err != nil {
			return nil, fmt.Errorf("bootstrapping admin %q: %w", login, err)
		}
	}
	return s, nil
}

func (s *RoleStore) hasAdminBindingForUser(login string) bool {
	for _, b := range s.bindings {
		if b.SubjectType == "user" && b.Role == RoleAdmin && strings.EqualFold(b.Subject, login) {
			return true
		}
	}
	return false
}

// List returns every binding, oldest first.
func (s *RoleStore) List() []RoleBinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RoleBinding, 0, len(s.bindings))
	for _, b := range s.bindings {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// Upsert validates and creates (b.ID == "") or replaces (b.ID already
// present) one binding, persisting it to archive if configured.
func (s *RoleStore) Upsert(b RoleBinding) (RoleBinding, error) {
	if b.SubjectType != "user" && b.SubjectType != "team" {
		return RoleBinding{}, fmt.Errorf("subjectType must be \"user\" or \"team\", got %q", b.SubjectType)
	}
	if strings.TrimSpace(b.Subject) == "" {
		return RoleBinding{}, fmt.Errorf("subject is required")
	}
	if b.SubjectType == "team" && !strings.Contains(b.Subject, "/") {
		return RoleBinding{}, fmt.Errorf("team subject must be \"org/team-slug\", got %q", b.Subject)
	}
	if _, err := ParseRole(string(b.Role)); err != nil {
		return RoleBinding{}, err
	}

	now := nowRFC3339()
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.ID == "" {
		b.ID = newRandomID("role")
		b.CreatedAt = now
	} else if existing, ok := s.bindings[b.ID]; ok {
		b.CreatedAt = existing.CreatedAt
	} else {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	s.bindings[b.ID] = b

	if s.archive != nil {
		if err := s.archive.UpsertRoleBinding(b); err != nil {
			delete(s.bindings, b.ID)
			return RoleBinding{}, err
		}
	}
	return b, nil
}

// Delete removes one binding by ID, reporting ok=false if it didn't exist.
func (s *RoleStore) Delete(id string) (ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok = s.bindings[id]; !ok {
		return false, nil
	}
	delete(s.bindings, id)
	if s.archive != nil {
		if err := s.archive.DeleteRoleBinding(id); err != nil {
			return false, err
		}
	}
	return true, nil
}

// getTeams/setTeams cache one username's GitHub team memberships (resolved
// at OAuth login time - see authCallbackHandler) for teamCacheTTL, so
// team-based bindings apply to a browser session without this service
// needing to hold that user's raw GitHub access token beyond the login
// request itself.
func (s *RoleStore) getTeams(username string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.teamCache[strings.ToLower(username)]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.teams, true
}

func (s *RoleStore) setTeams(username string, teams []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teamCache[strings.ToLower(username)] = teamCacheEntry{teams: teams, expires: time.Now().Add(teamCacheTTL)}
}

// RoleInfo is one request's resolved access level: computed once per
// request by EffectiveRole and stashed in its context (see withRoleInfo/
// roleInfoOrOpen), read by every handler that needs to gate a write or
// filter a list by agent.
type RoleInfo struct {
	Role Role
	// Agents, when non-nil, is the exhaustive set of agent names this
	// request may see - nil means unrestricted (every RoleAdmin request,
	// or any request with no Agents-scoped binding at its winning rank).
	Agents []string
	// Tenants, when non-nil, is the exhaustive set of tenants this request may see - nil means every tenant.
	Tenants []string
	IsAdmin bool
	// Unresolved is set when the request's team membership is unknown and a team binding could have changed
	// its role: the request is refused rather than given defaultRole.
	Unresolved bool
	// FromSender is set when a sender's ingest token authenticated the request: the agent under evaluation, not a
	// person. It may read its findings and register corrections, never label, since a label is the score of record.
	FromSender bool
}

// CanWrite reports whether info's role permits any mutating request
// (POST /api/evaluate, /api/evaluate/stream, /api/evalsets); every role
// except RoleViewer can.
func (info RoleInfo) CanWrite() bool {
	// Runs and eval sets carry no tenant, so a tenant-scoped role cannot create them without leaking into other
	// tenants'"'"' views.
	return (info.Role == RoleAdmin || info.Role == RoleMember) && !info.TenantScoped()
}

// Scoped reports whether info is limited to a set of agents.
func (info RoleInfo) Scoped() bool {
	return info.Agents != nil
}

// TenantScoped reports whether info is limited to a set of tenants.
func (info RoleInfo) TenantScoped() bool {
	return info.Tenants != nil
}

// AllowsAnyTenant reports whether info may see data belonging to any one of tenants. For a tenant-scoped role data
// with no tenant is not visible.
func (info RoleInfo) AllowsAnyTenant(tenants []string) bool {
	if !info.TenantScoped() {
		return true
	}
	for _, k := range tenants {
		if k == "" {
			continue
		}
		for _, p := range info.Tenants {
			if tenantPatternMatches(p, k) {
				return true
			}
		}
	}
	return false
}

// AllowsSession reports whether info may see a session with these agents and tenants: both scopes must allow.
func (info RoleInfo) AllowsSession(agents, tenants []string) bool {
	return info.AllowsAnyAgent(agents) && info.AllowsAnyTenant(tenants)
}

// AllowsAgent reports whether info may see a result attributed to agentName. A scoped role never sees a
// result whose agent is unknown: an empty name must not be a way around the scope.
func (info RoleInfo) AllowsAgent(agentName string) bool {
	if !info.Scoped() {
		return true
	}
	for _, a := range info.Agents {
		if a == agentName && a != "" {
			return true
		}
	}
	return false
}

// AllowsAnyAgent reports whether info may see a result attributed to any one of agentNames (a run or session
// can list more than one agent). For a scoped role an empty list means unknown, so hidden.
func (info RoleInfo) AllowsAnyAgent(agentNames []string) bool {
	if !info.Scoped() {
		return true
	}
	for _, a := range agentNames {
		if info.AllowsAgent(a) {
			return true
		}
	}
	return false
}

// runVisibleForRole hides a run from a scoped role unless one of the run's agents is in scope; a run that is
// still running (no Summary yet) or whose agent could not be resolved is hidden too. A tenant-limited role sees
// no run here: use runVisibleToRole with the run's tenant.
func runVisibleForRole(run runDTO, info RoleInfo) bool {
	return runVisibleToRole(run, info, nil)
}

// runVisibleToRole is runVisibleForRole for a run that may belong to tenants (see runTenants). A tenant-limited
// role sees a run only when it belongs to one of its tenants; both limits must allow.
func runVisibleToRole(run runDTO, info RoleInfo, tenants []string) bool {
	if info.TenantScoped() && !info.AllowsAnyTenant(tenants) {
		return false
	}
	if !info.Scoped() {
		return true
	}
	if run.Summary == nil {
		return false
	}
	return info.AllowsAnyAgent(run.Summary.Agents)
}

// denyScoped answers 403 and reports true for a scoped role on an endpoint whose data cannot be attributed to
// an agent (usage totals, eval sets, online scores): showing it would show every agent's data.
func denyScoped(w http.ResponseWriter, r *http.Request) bool {
	info := roleInfoOrOpen(r.Context())
	if !info.Scoped() && !info.TenantScoped() {
		return false
	}
	writeEvaluateError(w, http.StatusForbidden, "this view is not available to an agent- or tenant-scoped role")
	return true
}

// denyAgentScoped is denyScoped for views that tenants can be filtered on (usage, online scores): only a role
// scoped to agents is refused, since those rows say nothing about agents.
func denyAgentScoped(w http.ResponseWriter, r *http.Request) bool {
	if !roleInfoOrOpen(r.Context()).Scoped() {
		return false
	}
	writeEvaluateError(w, http.StatusForbidden, "this view is not available to an agent-scoped role")
	return true
}

// EffectiveRole resolves username's access level given the GitHub teams it's currently known to belong to
// (resolveTeamsForRequest - possibly none, if team membership couldn't be determined for this request).
// Bindings are matched by exact username or "org/slug" team subject; when more than one binding matches (direct
// + one or more teams), only bindings at the single highest rank among the matches apply - a RoleViewer team
// binding never silently narrows a RoleAdmin/RoleMember binding the same user also holds by name, which is the
// only interpretation that lets "grant this specific person admin, even though their team is scoped to viewer"
// work at all. Agents scopes at that winning rank are unioned; if any winning binding is itself unrestricted
// (empty Agents), the union is unrestricted too.
//
// When the teams are not known (teamsKnown false) and the user has no binding by name, a team binding they may
// belong to could narrow or raise their role, so the result is Unresolved instead of defaultRole.
func (s *RoleStore) EffectiveRole(username string, teams []string, teamsKnown bool) RoleInfo {
	s.mu.Lock()
	bindings := make([]RoleBinding, 0, len(s.bindings))
	for _, b := range s.bindings {
		bindings = append(bindings, b)
	}
	s.mu.Unlock()

	subjectKeys := map[string]bool{"user:" + strings.ToLower(username): true}
	for _, t := range teams {
		subjectKeys["team:"+strings.ToLower(t)] = true
	}

	var matched []RoleBinding
	for _, b := range bindings {
		if subjectKeys[b.subjectKey()] {
			matched = append(matched, b)
		}
	}
	if len(matched) == 0 {
		if !teamsKnown && hasTeamBinding(bindings) {
			return RoleInfo{Role: RoleViewer, Agents: []string{}, Tenants: []string{}, Unresolved: true}
		}
		if s.defaultRole == RoleNone {
			return RoleInfo{Role: RoleNone, Agents: []string{}, Tenants: []string{}}
		}
		return RoleInfo{Role: s.defaultRole, Agents: nil, IsAdmin: s.defaultRole == RoleAdmin}
	}

	best := matched[0].Role
	for _, b := range matched {
		if b.Role.rank() > best.rank() {
			best = b.Role
		}
	}

	agents := unionScope(matched, best, func(b RoleBinding) []string { return b.Agents })
	tenants := unionScope(matched, best, func(b RoleBinding) []string { return b.Tenants })
	if slices.Contains(tenants, "*") {
		tenants = nil // a grant on every tenant is no limit at all
	}
	// A limit takes admin away: an admin bound to some tenants or agents manages nothing, since the admin
	// endpoints list and change every sender, tenant and binding.
	return RoleInfo{Role: best, Agents: agents, Tenants: tenants, IsAdmin: best == RoleAdmin && agents == nil && tenants == nil}
}

// unionScope unions one scope list (agents or tenants) over the bindings at the winning rank; if any of them has an
// empty list it is unrestricted, so the result is nil.
func unionScope(matched []RoleBinding, best Role, list func(RoleBinding) []string) []string {
	set := map[string]bool{}
	for _, b := range matched {
		if b.Role != best {
			continue
		}
		l := list(b)
		if len(l) == 0 {
			return nil
		}
		for _, v := range l {
			set[v] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func hasTeamBinding(bindings []RoleBinding) bool {
	for _, b := range bindings {
		if b.SubjectType == "team" {
			return true
		}
	}
	return false
}

// HasTeamBindings reports whether any binding targets a team, i.e. whether a user's team membership matters.
func (s *RoleStore) HasTeamBindings() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bindings {
		if b.SubjectType == "team" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Request-context plumbing
// ---------------------------------------------------------------------------

type roleInfoContextKey struct{}

func withRoleInfo(ctx context.Context, info RoleInfo) context.Context {
	return context.WithValue(ctx, roleInfoContextKey{}, info)
}

func roleInfoFromContext(ctx context.Context) (RoleInfo, bool) {
	info, ok := ctx.Value(roleInfoContextKey{}).(RoleInfo)
	return info, ok
}

// roleInfoOrOpen returns ctx's resolved RoleInfo, or an unrestricted
// RoleAdmin when none was ever set - which is exactly the case whenever
// RBAC isn't configured at all (no --session-secret, so requireSession/
// withRole never even ran) or roleStore is nil for any other reason.
// Every handler that gates a write or filters by agent calls this rather
// than roleInfoFromContext directly, so "RBAC not configured" reliably
// means "behave exactly as before this file existed", not "block
// everything because there's no role to check".
func roleInfoOrOpen(ctx context.Context) RoleInfo {
	if info, ok := roleInfoFromContext(ctx); ok {
		return info
	}
	return RoleInfo{Role: RoleAdmin, IsAdmin: true}
}

// roleForIdentity resolves id's access level. A sender token is a member limited to the sender's tenants. An API token acts with the role it was minted with and nothing
// else, so its label does not pick up the role of a GitHub user of the same name. Everyone else gets the
// strongest matching role binding (see EffectiveRole); unknown team membership comes back Unresolved.
func roleForIdentity(r *http.Request, roleStore *RoleStore, org string, ghTokens *githubTokenValidator, id identity) RoleInfo {
	// A sender's ingest token reads its own tenants and nothing else; no role binding can widen it. A token bound to
	// one tenant reads that tenant only.
	if id.Kind == identitySender {
		grant := id.Sender + "/*"
		if id.SenderTenant != "" {
			grant = tenantKey(id.Sender, id.SenderTenant)
		}
		return RoleInfo{Role: RoleMember, Tenants: []string{grant}, FromSender: true}
	}
	if id.Kind == tokenKindAPI {
		info := RoleInfo{Role: id.Role, IsAdmin: id.Role == RoleAdmin && len(id.Tenants) == 0}
		if len(id.Tenants) > 0 {
			info.Tenants = id.Tenants
		}
		return info
	}
	teams, known := resolveTeamsForRequest(r, roleStore, org, ghTokens, id)
	return roleStore.EffectiveRole(id.Username, teams, known)
}

// withRole resolves the current request's RoleInfo (from the identity requireSession already put in context) and
// stashes it for downstream handlers. Must be the handler requireSession wraps (not the other way around): it
// depends on requireSession having already run. A nil roleStore (RBAC not configured - see server.go's Serve)
// passes the request through untouched; downstream handlers then see roleInfoOrOpen's unrestricted-admin
// default.
func withRole(roleStore *RoleStore, org string, ghTokens *githubTokenValidator, handler http.HandlerFunc) http.HandlerFunc {
	if roleStore == nil {
		return handler
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if id, ok := identityFromContext(r.Context()); ok {
			info := roleForIdentity(r, roleStore, org, ghTokens, id)
			if info.Unresolved {
				writeTeamsUnresolved(w, r)
				return
			}
			if info.Role == RoleNone {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "no access: no role binding covers you; ask an admin to add your team", "role": "none"})
				return
			}
			r = r.WithContext(withRoleInfo(r.Context(), info))
		}
		handler(w, r)
	}
}

// resolveTeamsForRequest returns the GitHub team memberships of id for this request and whether they are known.
// Prefers RoleStore's login-time cache (populated by authCallbackHandler - the only place this service ever
// holds a browser user's own GitHub access token); for a request bearing a raw GitHub token directly (the
// MCP/CLI path - see githubtoken.go), resolves live via ghTokens (itself cached, so repeat calls in one CLI/MCP
// session cost one GitHub API round trip, not one per call) and backfills RoleStore's cache.
//
// Unknown is not the same as "no teams": a browser session whose cache entry is gone (the pod restarted, the
// entry expired) and a GitHub token whose team lookup failed are unknown, and EffectiveRole refuses them rather
// than treating them as team-less.
func resolveTeamsForRequest(r *http.Request, roleStore *RoleStore, org string, ghTokens *githubTokenValidator, id identity) (teams []string, known bool) {
	if teams, ok := roleStore.getTeams(id.Username); ok {
		return teams, true
	}
	if org == "" || ghTokens == nil {
		return nil, true
	}
	if id.Kind != identityGitHub {
		return nil, false // a session cookie whose login-time team lookup is gone.
	}
	token := strings.TrimSpace(r.Header.Get("Authorization")[len("bearer "):]) // identityGitHub implies a bearer header.
	if _, ok := ghTokens.validate(token); !ok {
		return nil, false
	}
	teams, known = ghTokens.teamsForCached(token)
	if known {
		roleStore.setTeams(id.Username, teams)
	}
	return teams, known
}

// writeTeamsUnresolved refuses a request whose team membership could not be determined. A browser session is
// told to sign in again, which repopulates its teams; a token holder is told to retry.
func writeTeamsUnresolved(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(sessionCookieName); err == nil && r.Header.Get("Authorization") == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false, "error": "team membership is unknown; sign in again"})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "team membership could not be resolved; retry shortly"})
}
