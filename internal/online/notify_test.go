package online

import (
	"context"
	"net/http"
	neturl "net/url"
	"strings"
	"testing"
	"time"
)

func TestChatTextNeutralisesSpanControlledValues(t *testing.T) {
	in := "x`\n@everyone [click](http://evil.test) **bold** " + strings.Repeat("a", 200)
	got := chatText(in)
	for _, bad := range []string{"`", "\n", "@", "[", "]", "*"} {
		if strings.Contains(got, bad) {
			t.Errorf("chatText kept %q: %q", bad, got)
		}
	}
	if n := len([]rune(got)); n > 81 {
		t.Errorf("chatText returned %d runes, want at most 81", n)
	}
	if chatText("jev_grounded") != "jev_grounded" {
		t.Error("an ordinary metric name must pass through unchanged")
	}

	text := DiscordText(AlertPayload{Event: EventOpen, Tenant: "t`\n@everyone", Kind: "k", Metric: "m`]", Sessions: []string{"s`1\n"}})
	if strings.Count(text, "`")%2 != 0 || strings.Contains(text, "@everyone") {
		t.Errorf("alert text still carries span-controlled markup: %q", text)
	}
}

func TestNotifierErrorsDoNotCarryTheWebhookURL(t *testing.T) {
	const secret = "https://discord.example.invalid/api/webhooks/123/SECRET-TOKEN"
	n := &Notifier{Sinks: []Sink{{Type: "discord", URL: secret}}, Client: &http.Client{Timeout: time.Second}}
	err := n.Alert(context.Background(), AlertPayload{})
	if err == nil {
		t.Fatal("want a delivery error")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") || strings.Contains(err.Error(), "webhooks") {
		t.Errorf("error carries the webhook URL: %v", err)
	}
}

func TestSinkBearerTokenTravelsOnlyOverSafeTransport(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://hooks.example.com/x":          true,
		"http://localhost:8080/x":              true,
		"http://127.0.0.1/x":                   true,
		"http://sink.ns.svc/x":                 true,
		"http://sink.ns.svc.cluster.local:9/x": true,
		"http://hooks.example.com/x":           false,
		"ftp://hooks.example.com/x":            false,
	} {
		u, _ := neturl.Parse(raw)
		if got := bearerMayTravel(u); got != want {
			t.Errorf("%s: %v, want %v", raw, got, want)
		}
	}
}
