package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/triageagent-dev/ego/internal/api"
	"github.com/triageagent-dev/ego/internal/mcptools"
)

func TestConfineToRoot(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	inside := filepath.Join(root, "trace.json")
	secret := filepath.Join(outside, "secret.json")
	for _, p := range []string{inside, secret} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(secret, filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}

	if got, err := confineToRoot(root, inside); err != nil || got != inside {
		t.Fatalf("file inside the root: %q, %v", got, err)
	}
	if got, err := confineToRoot(root, "trace.json"); err != nil || got != inside {
		t.Fatalf("relative path inside the root: %q, %v", got, err)
	}
	for name, p := range map[string]string{
		"absolute outside": secret,
		"dot-dot":          filepath.Join(root, "..", filepath.Base(outside), "secret.json"),
		"symlink out":      filepath.Join(root, "link.json"),
		"missing":          filepath.Join(root, "nope.json"),
	} {
		if _, err := confineToRoot(root, p); err == nil {
			t.Errorf("%s: accepted %s", name, p)
		}
	}
}

func TestTokenSafeFor(t *testing.T) {
	for url, want := range map[string]bool{
		"https://ego.example.com":  true,
		"http://localhost:8001":    true,
		"http://127.0.0.1:8001":    true,
		"http://[::1]:8001":        true,
		"http://ego.example.com":   false,
		"http://localhost.evil.io": false,
		"ftp://localhost":          false,
		"://bad":                   false,
	} {
		if got := tokenSafeFor(url); got != want {
			t.Errorf("tokenSafeFor(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestRoleToolsNeedAdminOption(t *testing.T) {
	has := func(opts mcpOptions, name string) bool {
		return newMCPServer(mcptools.NewREST("", "", nil), opts).GetTool(name) != nil
	}
	for _, name := range []string{"list_role_bindings", "set_role_binding", "delete_role_binding"} {
		if has(mcpOptions{}, name) {
			t.Errorf("%s is registered without --admin-tools", name)
		}
		if !has(mcpOptions{adminTools: true}, name) {
			t.Errorf("%s is missing with --admin-tools", name)
		}
	}
	if !has(mcpOptions{}, "list_sessions") {
		t.Error("read-only tools must stay available")
	}
}

// The stdio command and the MCP endpoint of serve register one registry: the same tools, but for evaluate_traces,
// which reads local files and so is stdio only.
func TestStdioToolsAreServeToolsPlusEvaluateTraces(t *testing.T) {
	rest := mcptools.NewREST("", "", nil)
	var stdio, serve []string
	for name := range newMCPServer(rest, mcpOptions{adminTools: true}).ListTools() {
		stdio = append(stdio, name)
	}
	for _, tool := range mcptools.Tools(rest, api.MCPToolOptions()) {
		serve = append(serve, tool.Tool.Name)
	}
	sort.Strings(stdio)
	sort.Strings(serve)
	want := append(append([]string{}, serve...), "evaluate_traces")
	sort.Strings(want)
	if !slices.Equal(stdio, want) {
		t.Errorf("stdio tools = %v\nserve tools + evaluate_traces = %v", stdio, want)
	}
	if slices.Contains(serve, "evaluate_traces") {
		t.Error("serve registers evaluate_traces")
	}
}
