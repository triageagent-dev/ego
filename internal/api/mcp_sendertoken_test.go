package api

import (
	"strings"
	"testing"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

func TestMintSenderTokenTool(t *testing.T) {
	senders, err := NewSenderStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	names, _ := NewTenantNames(nil)
	if _, err := senders.Create("triage", "Triage"); err != nil {
		t.Fatal(err)
	}
	b := &mcpBackend{senders: senders, names: names}
	admin := asRole(RoleInfo{Role: RoleAdmin, IsAdmin: true})
	in := mcptools.SenderTokenInput{Sender: "triage", Label: "rc spot", Tenant: "acme", TenantName: "Acme", MCP: true}

	out, err := b.MintSenderToken(admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Token, "egs_") || out.Tenant != "acme" || !out.MCP || out.Label != "rc spot" {
		t.Fatalf("token = %+v", out)
	}
	got, ok := senders.Resolve(out.Token)
	if !ok || got.Sender != "triage" || got.Tenant != "acme" {
		t.Fatalf("the minted token does not resolve to its sender and tenant: %+v %v", got, ok)
	}
	if n := names.Name("triage/acme"); n != "Acme" {
		t.Errorf("tenant name = %q", n)
	}

	// Only an admin mints; a member limited to the sender cannot widen its own access.
	member := asRole(RoleInfo{Role: RoleMember, Tenants: []string{"triage/*"}})
	if _, err := b.MintSenderToken(member, in); err == nil {
		t.Error("a member minted an ingest token")
	}
	if _, err := b.MintSenderToken(admin, mcptools.SenderTokenInput{Sender: "nope"}); err == nil {
		t.Error("a token was minted for an unknown sender")
	}
	if _, err := b.MintSenderToken(admin, mcptools.SenderTokenInput{Sender: "triage", TenantName: "x"}); err == nil {
		t.Error("a tenant name without a tenant was accepted")
	}
	// A new source registers its sender with its first token, once; a bad id registers nothing.
	fresh, err := b.MintSenderToken(admin, mcptools.SenderTokenInput{Sender: "symbio-rag", Tenant: "symbio-rag", CreateSender: true})
	if err != nil || fresh.Sender != "symbio-rag" || !senders.Exists("symbio-rag") {
		t.Fatalf("create_sender: %+v %v", fresh, err)
	}
	if _, err := b.MintSenderToken(admin, mcptools.SenderTokenInput{Sender: "symbio-rag", CreateSender: true}); err != nil {
		t.Errorf("a second token for a sender that exists: %v", err)
	}
	if _, err := b.MintSenderToken(admin, mcptools.SenderTokenInput{Sender: "Bad Id", CreateSender: true}); err == nil || senders.Exists("Bad Id") {
		t.Error("an invalid sender id was registered")
	}
	if n := len(senders.Tokens("triage")); n != 1 {
		t.Errorf("%d tokens after the refused calls, want 1", n)
	}
}

func TestRevokeSenderTokenTool(t *testing.T) {
	senders, err := NewSenderStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	names, _ := NewTenantNames(nil)
	if _, err := senders.Create("symbio-rag", ""); err != nil {
		t.Fatal(err)
	}
	b := &mcpBackend{senders: senders, names: names}
	admin := asRole(RoleInfo{Role: RoleAdmin, IsAdmin: true})
	first, err := b.MintSenderToken(admin, mcptools.SenderTokenInput{Sender: "symbio-rag", Tenant: "symbio-rag"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.MintSenderToken(admin, mcptools.SenderTokenInput{Sender: "symbio-rag", Tenant: "symbio-rag", MCP: true})
	if err != nil {
		t.Fatal(err)
	}

	member := asRole(RoleInfo{Role: RoleMember, Tenants: []string{"symbio-rag/*"}})
	if _, err := b.RevokeSenderToken(member, "symbio-rag", first.ID); err == nil {
		t.Fatal("a member revoked a token")
	}
	if _, ok := senders.Resolve(first.Token); !ok {
		t.Fatal("a refused revoke removed the token")
	}
	if _, err := b.RevokeSenderToken(admin, "other", first.ID); err == nil {
		t.Error("revoked a token through another sender")
	}
	if out, err := b.RevokeSenderToken(admin, "symbio-rag", first.ID); err != nil || !out["revoked"] {
		t.Fatalf("revoke = %v %v", out, err)
	}
	if _, ok := senders.Resolve(first.Token); ok {
		t.Error("the revoked token still resolves")
	}
	if _, ok := senders.Resolve(second.Token); !ok {
		t.Error("revoking one token took the other with it")
	}
	if _, err := b.RevokeSenderToken(admin, "symbio-rag", first.ID); err == nil {
		t.Error("revoking twice must say the token is gone")
	}
}
