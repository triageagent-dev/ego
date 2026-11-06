package api

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// A change of access leaves one log line naming who made it, and the line never carries a token value.
func TestAdminChangesAreLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	senders, err := NewSenderStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	names, _ := NewTenantNames(nil)
	b := &mcpBackend{senders: senders, names: names}
	ctx := withIdentity(asRole(RoleInfo{Role: RoleAdmin, IsAdmin: true}), identity{Username: "alice", Kind: identityGitHub})

	out, err := b.MintSenderToken(ctx, mcptools.SenderTokenInput{Sender: "triage", Tenant: "acme", CreateSender: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.RevokeSenderToken(ctx, "triage", out.ID); err != nil {
		t.Fatal(err)
	}

	log := buf.String()
	for _, want := range []string{"action=sender.create", "action=token.mint", "action=token.revoke", "actor=alice", "tokenId=" + out.ID} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, out.Token) {
		t.Errorf("the log carries the token value:\n%s", log)
	}
}
