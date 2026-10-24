package api

import (
	"strings"
	"testing"
)

func TestCleanLabelKeepsKeysAndIdsUnambiguous(t *testing.T) {
	for in, want := range map[string]string{
		"acme":                  "acme",
		"01a169c0-14a2.x_y@z+1": "01a169c0-14a2.x_y@z+1",
		"core/acme":             "core_acme",
		"a:b":                   "a_b",
		"*":                     "_",
		"x\x1fvictim/t":         "x_victim_t",
		"a b|c,d":               "a_b_c_d",
		"  padded  ":            "padded",
		"München":               "München",
		"x‮y":                   "x_y",
	} {
		if got := cleanLabel(in); got != want {
			t.Errorf("cleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanLabel(strings.Repeat("a", 500)); len(got) != maxLabelLen {
		t.Errorf("a long label is cut to %d, got %d", maxLabelLen, len(got))
	}
	for _, bad := range []string{"", "*", "a/b", "a:b", "a b", "a|b", "x\x1fy"} {
		if err := ValidTenantLabel(bad); err == nil {
			t.Errorf("ValidTenantLabel(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"t1", "demo-1", "01a169c0-14a2", "a.b_c@d+e"} {
		if err := ValidTenantLabel(ok); err != nil {
			t.Errorf("ValidTenantLabel(%q) = %v", ok, err)
		}
	}
	if got := cleanIdentity("agent\x1f‮name\x00"); got != "agentname" {
		t.Errorf("cleanIdentity = %q", got)
	}
}

// A label or an agent name carrying the index separator cannot make one sender's session appear under another
// sender's tenant, and two tenants of one sender cannot reach each other's session through an ambiguous id.
func TestIngestCannotSpellAnotherTenantOrSession(t *testing.T) {
	store := NewSessionStore(nil)
	store.IngestFor("evil", labelledTracesBody("s", "aaaa", "s1", "x\x1fvictim/t"))
	for _, sum := range store.List() {
		for _, k := range sum.Tenants {
			if strings.Contains(k, "\x1f") || k != "evil/x_victim_t" {
				t.Errorf("tenant key %q, want evil/x_victim_t", k)
			}
		}
	}
	rows := indexRowOf(store.sessions[store.order[0]])
	if got := splitIndexList(joinIndexList(rows.Tenants)); len(got) != 1 || got[0] != "evil/x_victim_t" {
		t.Errorf("index round trip = %v, want one key", got)
	}

	// Session ids are built as sender:label:name; labels hold no colon now, so these two cannot meet.
	store2 := NewSessionStore(nil)
	store2.IngestFor("core", labelledTracesBody("x:y", "bbbb", "s1", "t1"))
	store2.IngestFor("core", labelledTracesBody("y", "cccc", "s2", "t1:x"))
	ids := idsOf(store2)
	if len(ids) != 2 {
		t.Errorf("sessions = %v, want two distinct sessions", ids)
	}
}

// A trace of another tenant that lands on an existing session id is refused.
func TestSessionRefusesATraceOfAnotherTenant(t *testing.T) {
	store := NewSessionStore(nil)
	store.IngestFor("core", labelledTracesBody("same", "aaaa", "s1", "t1"))
	id := store.order[0]
	// Force the clash an ambiguous id would have caused: the session now answers to another tenant's trace.
	store.sessions[id].tenantKey = "core/t1"
	body := labelledTracesBody("same", "bbbb", "s2", "t1")
	if n := store.IngestFor("core", body); n == 0 {
		t.Fatal("a trace of the same tenant was refused")
	}
	store.sessions[id].tenantKey = "core/other"
	before := len(store.sessions[id].Spans)
	store.IngestFor("core", labelledTracesBody("same", "cccc", "s3", "t1"))
	if after := len(store.sessions[id].Spans); after != before {
		t.Errorf("spans %d -> %d: a trace of another tenant was added", before, after)
	}
}

func TestTenantNamesRejectLookAlikesTruncateAndAreBounded(t *testing.T) {
	names, _ := NewTenantNames(nil)
	for _, bad := range []string{"bad‮name", "zero​width", "line break"} {
		if err := names.Set("k", bad); err == nil {
			t.Errorf("Set accepted %q", bad)
		}
	}
	names.Fill("sender/long", strings.Repeat("é", 200))
	if got := names.Name("sender/long"); len([]rune(got)) != maxTenantNameLen {
		t.Errorf("a long name is cut to %d characters, got %d", maxTenantNameLen, len([]rune(got)))
	}
	names.Fill("sender/bidi", "evil‮name")
	if got := names.Name("sender/bidi"); got != "" {
		t.Errorf("a name with a format character was kept: %q", got)
	}
}

func TestLedgerClampsTokenValues(t *testing.T) {
	for in, want := range map[int64]int64{-5: 0, 0: 0, 10: 10, 1 << 40: maxCallTokens} {
		if got := clampTokens(in); got != want {
			t.Errorf("clampTokens(%d) = %d, want %d", in, got, want)
		}
	}
}

// One sender cannot open more sessions or tenants than its limits; another sender is not affected.
func TestSenderQuotasRefuseNewSessionsAndTenantsPastTheLimit(t *testing.T) {
	oldOpen, oldTenants := maxOpenSessionsPerSender, maxTenantsPerSender
	defer func() { maxOpenSessionsPerSender, maxTenantsPerSender = oldOpen, oldTenants }()
	maxOpenSessionsPerSender, maxTenantsPerSender = 3, 2

	store := NewSessionStore(nil)
	store.IngestFor("a", labelledTracesBody("s1", "t001", "p1", "x"))
	store.IngestFor("a", labelledTracesBody("s2", "t002", "p2", "y"))
	// A third tenant is refused: two are the limit.
	store.IngestFor("a", labelledTracesBody("s3", "t003", "p3", "z"))
	// An existing session still takes spans.
	if n := store.IngestFor("a", labelledTracesBody("s1", "t004", "p4", "x")); n == 0 {
		t.Error("an existing session was refused")
	}
	// A third open session of an existing tenant is allowed; a fourth is over the limit of three.
	store.IngestFor("a", labelledTracesBody("s4", "t005", "p5", "x"))
	store.IngestFor("a", labelledTracesBody("s5", "t006", "p6", "x"))
	got := 0
	for _, sum := range store.List() {
		if strings.HasPrefix(sum.SessionID, "a:") {
			got++
		}
	}
	if got != 3 {
		t.Errorf("sender a has %d sessions, want the limit of 3", got)
	}
	// Another sender has its own limits.
	store.IngestFor("b", labelledTracesBody("s1", "t007", "p7", "x"))
	if _, ok := store.Summary("b:x:s1"); !ok {
		t.Error("sender b was refused because of sender a")
	}
}

func TestTenantNamesPerSenderAreBounded(t *testing.T) {
	old := maxNamesPerSender
	defer func() { maxNamesPerSender = old }()
	maxNamesPerSender = 2
	names, _ := NewTenantNames(nil)
	for _, k := range []string{"s/a", "s/b", "s/c", "other/a"} {
		names.Fill(k, "n-"+k)
	}
	if names.Name("s/c") != "" {
		t.Error("a third name of one sender was kept")
	}
	if names.Name("other/a") == "" {
		t.Error("another sender's name was refused")
	}
}
