package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/triageagent-dev/ego/internal/api"
)

// authCmd dispatches `ego auth <subcommand>`.
func authCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ego auth mint-token --name <label> [--role member] [--tenants a,b] [--ttl-days 90] [--secret ...]")
	}
	switch args[0] {
	case "mint-token":
		return authMintTokenCmd(args[1:])
	default:
		return fmt.Errorf("ego auth: unknown subcommand %q (only mint-token exists)", args[0])
	}
}

// Accepted wherever a session cookie is: internal/api's requireSession Authorization: Bearer path, or the
// cookie itself.
func authMintTokenCmd(args []string) error {
	fs := flag.NewFlagSet("auth mint-token", flag.ExitOnError)
	name := fs.String("name", "", "label for the token (e.g. 'triage-core-otlp' or 'claude-code-mcp'), embedded as the token's subject and logged wherever it authenticates - not a GitHub username, since non-browser clients have no GitHub identity of their own (required)")
	role := fs.String("role", "member", "role the token acts with: admin, member or viewer. It is not looked up from role bindings, so a label equal to a GitHub login does not inherit that user's role")
	tenantsFlag := fs.String("tenants", "", "comma-separated tenants the token is limited to: <sender>/<tenant>, <sender>/* or *; it then sees only their data (empty: every tenant)")
	ttlDays := fs.Int("ttl-days", 90, "token lifetime in days (default 90); machine clients have no login flow to refresh it, so mint a new one before it ends - revoke one with POST /api/admin/revocations (the id is printed on stderr), or rotate --secret/EGO_SESSION_SECRET to invalidate every token and cookie at once")
	secret := fs.String("secret", env("SESSION_SECRET"), "signing secret; falls back to EGO_SESSION_SECRET so this can run against the same secret the deployed server uses without pasting it into a flag")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *name == "" {
		return fmt.Errorf("--name is required")
	}
	if *secret == "" {
		return fmt.Errorf("no signing secret: pass --secret or set EGO_SESSION_SECRET (the same value the running server has)")
	}
	if err := checkSessionSecret(*secret); err != nil {
		return err
	}
	if *ttlDays < 1 {
		return fmt.Errorf("--ttl-days must be at least 1")
	}

	tokenRole, err := api.ParseRole(*role)
	if err != nil {
		return err
	}
	tenants, err := api.ParseTenantList(*tenantsFlag)
	if err != nil {
		return err
	}
	token, id := api.MintAPIToken(*name, *secret, tokenRole, tenants, time.Duration(*ttlDays)*24*time.Hour)
	fmt.Fprintf(os.Stderr, "token id: %s (role %s, valid %d days)\n", id, tokenRole, *ttlDays)
	fmt.Println(token)
	return nil
}
