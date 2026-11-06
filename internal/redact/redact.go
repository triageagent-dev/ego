// Package redact masks credentials and email addresses in trace text before it leaves for an external model: the
// judge, the decision service and Vertex's managed evaluation. Traces carry whatever the agent's users typed and
// every tool returned, so a pasted API key or a customer's address would otherwise be sent to a third party.
package redact

import (
	"fmt"
	"regexp"
	"strings"
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
	// PEM private keys, in plain text or with escaped newlines.
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[^"]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// authorization is an Authorization header's credential. "basic" and "bearer" are also ordinary words, so the match
// is masked only when the value after them looks like a credential (credentialLike) and not like the next word.
var authorization = regexp.MustCompile(`(?i)\b(bearer|basic)\s+([A-Za-z0-9._~+/=-]{16,})`)

// keyValue is a password or secret given as key=value or key: value, in plain text, in JSON, or in JSON encoded
// inside a JSON string (the quotes then read \"). The value stops at whitespace, a quote, a backslash and the JSON
// punctuation, so the text around it, and an encoded body, stay as they were; the key stays readable.
var keyValue = regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token)\b((?:\\?["'])?\s*[:=]\s*(?:\\?["'])?)([^\s"'\\,;{}\[\]]{6,})`)

// maskValue masks a key=value match, keeping the key and its separator. An unquoted number (a JSON number) gets a
// quoted mask, so an encoded body stays a document.
func maskValue(m string) string {
	p := keyValue.FindStringSubmatch(m)
	mask := Mask
	quoted := strings.HasSuffix(p[2], `"`) || strings.HasSuffix(p[2], `'`) // the value's own opening quote
	if !quoted && strings.Trim(p[3], "0123456789.") == "" {
		mask = `"` + Mask + `"`
	}
	return p[1] + p[2] + mask
}

var email = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)

// credentialLike tells a token from a word: a credential of 16 characters or more holds a digit or a base64 mark;
// "internationalization" holds neither.
func credentialLike(s string) bool { return strings.ContainsAny(s, "0123456789+/=._~") }

// Redactor masks text. The zero value masks nothing.
type Redactor struct {
	enabled  bool
	patterns []*regexp.Regexp // masked whole
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
	r := &Redactor{enabled: true, patterns: append([]*regexp.Regexp(nil), builtin...)}
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
	if r == nil || !r.enabled {
		return s
	}
	for _, re := range r.patterns {
		s = re.ReplaceAllString(s, Mask)
	}
	s = authorization.ReplaceAllStringFunc(s, func(m string) string {
		parts := authorization.FindStringSubmatch(m)
		if !credentialLike(parts[2]) {
			return m
		}
		return parts[1] + " " + Mask
	})
	return keyValue.ReplaceAllStringFunc(s, maskValue)
}

// Bytes is Text for an encoded request body.
func (r *Redactor) Bytes(b []byte) []byte {
	if r == nil || !r.enabled {
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
