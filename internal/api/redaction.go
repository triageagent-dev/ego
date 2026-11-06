package api

import (
	"github.com/triageagent-dev/ego/internal/logx"
	"github.com/triageagent-dev/ego/internal/online"
	"github.com/triageagent-dev/ego/internal/redact"
)

// useRedaction makes the config's redaction the one every outbound model call applies. The config was validated when it
// was loaded, so a pattern that fails here keeps the redactor that was in force.
func useRedaction(cfg *online.Config) {
	r, err := redact.New(cfg.Redact)
	if err != nil {
		logx.Errorf("online: redaction config not applied: %v", err)
		return
	}
	redact.Use(r)
}
