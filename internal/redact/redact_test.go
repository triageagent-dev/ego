package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuiltinPatternsMaskCredentialsAndEmails(t *testing.T) {
	r, err := New(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{
		"ghp_0123456789abcdefghijklmnopqrstuvwxyz",
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz",
		"egs_Ab3dEfGhIjKlMnOpQrStUvWx",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789",
		"AIzaSyA-1234567890abcdefghijklmnopqrstu",
		"AKIAIOSFODNN7EXAMPLE",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----",
		"alice.smith@example.com",
	}
	for _, s := range secrets {
		got := r.Text("before " + s + " after")
		if strings.Contains(got, s) || !strings.Contains(got, Mask) || !strings.HasPrefix(got, "before ") || !strings.HasSuffix(got, " after") {
			t.Errorf("not masked cleanly: %q -> %q", s, got)
		}
	}
	if got := r.Text("Authorization: Bearer abcdef0123456789ABCDEF.token"); strings.Contains(got, "abcdef0123456789") {
		t.Errorf("bearer credential kept: %q", got)
	}
	if got := r.Text(`config: {"password": "hunter2hunter2", "user": "bob"}`); strings.Contains(got, "hunter2") || !strings.Contains(got, "password") || !strings.Contains(got, `"user": "bob"`) {
		t.Errorf("password value not masked, or the key and its neighbours lost: %q", got)
	}
}

func TestOrdinaryTextIsLeftAlone(t *testing.T) {
	r, _ := New(Defaults())
	for _, s := range []string{
		"The pod checkout-7d9f restarted 3 times; memory is at 90 percent.",
		"kubectl get pods -n triage-xray",
		"commit 5489f82468694e37c870809b790cfdb216993f641ccd95c8166953a336279e94",
		"Set the password in the Secret, never in the Deployment.",
	} {
		if got := r.Text(s); got != s {
			t.Errorf("changed ordinary text: %q -> %q", s, got)
		}
	}
}

func TestMaskingAnEncodedBodyKeepsItValidJSON(t *testing.T) {
	r, _ := New(Defaults())
	body, _ := json.Marshal(map[string]any{
		"state": map[string]any{"user": "mail me at bob@example.com", "key": "ghp_0123456789abcdefghijklmnopqrstuvwxyz",
			"pem": "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----", "auth": "Authorization: Bearer abcdef0123456789ABCDEF"},
	})
	out := r.Bytes(body)
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("masked body is not JSON: %v\n%s", err, out)
	}
	for _, leaked := range []string{"bob@example.com", "ghp_0123", "BEGIN PRIVATE KEY", "abcdef0123456789"} {
		if strings.Contains(string(out), leaked) {
			t.Errorf("%q survived in %s", leaked, out)
		}
	}
}

func TestConfigSwitchesAndExtraPatterns(t *testing.T) {
	off, _ := New(Config{})
	if got := off.Text("bob@example.com ghp_0123456789abcdefghijklmnopqrstuvwxyz"); strings.Contains(got, Mask) {
		t.Errorf("a disabled redactor masked: %q", got)
	}
	noMail, _ := New(Config{Enabled: true})
	if got := noMail.Text("bob@example.com"); got != "bob@example.com" {
		t.Errorf("emails masked with emails off: %q", got)
	}
	custom, err := New(Config{Enabled: true, Patterns: []string{`ACME-[0-9]{6}`}})
	if err != nil {
		t.Fatal(err)
	}
	if got := custom.Text("account ACME-123456 owes"); strings.Contains(got, "123456") {
		t.Errorf("extra pattern ignored: %q", got)
	}
	if _, err := New(Config{Enabled: true, Patterns: []string{`(`}}); err == nil {
		t.Error("an invalid pattern was accepted")
	}
}
