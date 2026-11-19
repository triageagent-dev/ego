package api

import (
	"path/filepath"
	"testing"
	"time"
)

// A login is pinned to the first account seen under it: another account with the same login is refused until an
// admin forgets the pin, and the pin survives a restart.
func TestIdentityPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	pins := newIdentityPins(store)
	for _, c := range []struct {
		login string
		id    int64
		want  bool
	}{
		{"Octocat", 1, true},
		{"octocat", 1, true},
		{"octocat", 2, false},
		{"octocat", 0, true}, // GitHub sent no id: nothing to compare.
		{"other", 2, true},
	} {
		if got, err := pins.check(c.login, c.id); err != nil || got != c.want {
			t.Errorf("check(%q, %d) = %v, %v; want %v", c.login, c.id, got, err, c.want)
		}
	}
	store.Close()

	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pins = newIdentityPins(store)
	if got, _ := pins.check("octocat", 2); got {
		t.Error("the pin did not survive a restart")
	}
	if err := pins.forget("OCTOCAT"); err != nil {
		t.Fatal(err)
	}
	if got, _ := pins.check("octocat", 2); !got {
		t.Error("a forgotten login did not take the next account")
	}
	if got, _ := (*identityPins)(nil).check("anyone", 5); !got {
		t.Error("a nil store refused a sign-in")
	}
}

// With listedModelsOnly a member may name only a listed judge model; an admin any.
func TestEvalGuardListedModelsOnly(t *testing.T) {
	limits := EvalLimits{ListedModelsOnly: true, Models: map[string]ModelLimit{"*": {Weight: 2}, "cheap-model": {}}}
	if err := limits.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (EvalLimits{ListedModelsOnly: true, Models: map[string]ModelLimit{"*": {}}}).Validate(); err == nil {
		t.Error("listedModelsOnly with no model listed by name was accepted")
	}
	g := newEvalGuard(0)
	g.setLimits(limits)
	now := time.Now()
	if _, status, _ := g.admit(now, "member", false, "dear-model"); status != 403 {
		t.Errorf("a member naming an unlisted model: status %d, want 403", status)
	}
	if _, status, _ := g.admit(now, "member", false, "*"); status != 403 {
		t.Errorf("a member naming *: status %d, want 403", status)
	}
	if release, status, msg := g.admit(now, "member", false, "cheap-model"); status != 0 {
		t.Errorf("a member naming a listed model: %d %s", status, msg)
	} else {
		release()
	}
	if release, status, msg := g.admit(now, "boss", true, "dear-model"); status != 0 {
		t.Errorf("an admin naming an unlisted model: %d %s", status, msg)
	} else {
		release()
	}
}
