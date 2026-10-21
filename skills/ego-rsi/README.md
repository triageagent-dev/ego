# ego-rsi

The skill that lets an agent read ego's findings, correct itself and have ego verify the correction (`docs/rsi.md`).
Install it by copying this folder to `~/.claude/skills/ego-rsi` (Claude Code) or into your client's skills directory.
Or skip the copy: connect the client to ego's MCP endpoint (`/mcp`) with the token your agent sends traces with and load the `rsi_loop` prompt.
