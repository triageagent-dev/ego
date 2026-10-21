// Package skills carries the agent-facing skill files in the binary, so the MCP prompt that serves them needs
// no copy of the repository next to it. The files stay the single source: edit them here, never a copy.
package skills

import _ "embed"

// EgoRSI is skills/ego-rsi/SKILL.md, the text of the `rsi_loop` MCP prompt.
//
//go:embed ego-rsi/SKILL.md
var EgoRSI string
