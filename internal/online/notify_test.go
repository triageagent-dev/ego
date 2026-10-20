package online

import (
	"strings"
	"testing"
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
