// Package redact masks credentials and email addresses in trace text before it leaves for an external model: the
// judge, the decision service and Vertex's managed evaluation. Traces carry whatever the agent's users typed and
// every tool returned, so a pasted API key or a customer's address would otherwise be sent to a third party.
package redact

import (
	"fmt"
	"regexp"
	"sync/atomic"
)

// Mask replaces what a pattern matched.
const Mask = "[REDACTED]"

// builtin patterns match values that are credentials by their shape. They are written to match inside JSON text as
// well as plain text: none of them can span a double quote, so masking an encoded request body leaves it valid JSON.
var builtin = []*regexp.Regexp{
	// GitHub tokens, ego's own sender tokens, and fine-grained personal access tokens.
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,255}|github_pat_[A-Za-z0-9_]{20,255}|egs_[A-Za-z0-9_-]{20,})`),
	// API keys of the common providers: sk-..., Google (AIza...), Slack (xox...), AWS access key ids.
	regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|xox[abprs]-[A-Za-z0-9-]{10,}|(?:AKIA|ASIA)[0-9A-Z]{16})\b`),
	// JSON web tokens: three base64url parts, the first two starting like a JSON object.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	// An Authorization header's credential.
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{16,}`),
	// PEM private keys, in plain text or with escaped newlines.
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[^"]*?-----END [A-Z ]*PRIVATE KEY-----`),
	// A password or secret given as key=value or key: value.
	regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token)\b(["']?\s*[:=]\s*["']?)[^\s"',;]{6,}`),
}

var email = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)

// Redactor masks text. The zero value masks nothing.
type Redactor struct {
	patterns []*regexp.Regexp
	keyValue int // index of the key=value pattern, whose key stays readable
}

// Config is what an operator can change.
type Config struct {
	// Enabled false sends trace text as it is. On by default.
	Enabled bool `json:"enabled"`
	// Emails masks email addresses too. On by default.
	Emails bool `json:"emails"`
	// Patterns are extra regular expressions (Go syntax) whose matches are masked, for identifiers of your own.
	Patterns []string `json:"patterns,omitempty"`
}

// Defaults is the configuration of an operator who says nothing.
func Defaults() Config { return Config{Enabled: true, Emails: true} }

// New builds a Redactor from a configuration; an invalid extra pattern is an error.
func New(c Config) (*Redactor, error) {
	if !c.Enabled {
		return &Redactor{}, nil
	}
	r := &Redactor{patterns: append([]*regexp.Regexp(nil), builtin...), keyValue: len(builtin) - 1}
	if c.Emails {
		r.patterns = append(r.patterns, email)
	}
	for _, p := range c.Patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("redact pattern %q: %w", p, err)
		}
		r.patterns = append(r.patterns, re)
	}
	return r, nil
}

// Text returns s with every match masked.
func (r *Redactor) Text(s string) string {
	if r == nil {
		return s
	}
	for i, re := range r.patterns {
		if i == r.keyValue {
			s = re.ReplaceAllString(s, "${1}${2}"+Mask)
			continue
		}
		s = re.ReplaceAllString(s, Mask)
	}
	return s
}

// Bytes is Text for an encoded request body.
func (r *Redactor) Bytes(b []byte) []byte {
	if r == nil || len(r.patterns) == 0 {
		return b
	}
	return []byte(r.Text(string(b)))
}

// current is the redactor the outbound clients use. It masks with the defaults until a config replaces it.
var current atomic.Pointer[Redactor]

func init() {
	r, _ := New(Defaults())
	current.Store(r)
}

// Use makes r the redactor of the outbound clients.
func Use(r *Redactor) { current.Store(r) }

// Text masks s with the current redactor.
func Text(s string) string { return current.Load().Text(s) }

// Bytes masks an encoded request body with the current redactor.
func Bytes(b []byte) []byte { return current.Load().Bytes(b) }
