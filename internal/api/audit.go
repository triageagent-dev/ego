package api

import (
	"context"
	"log/slog"
)

// auditLog records who changed access: a role binding set or removed, an ingest token or sender created, minted or
// revoked, a revocation recorded. One line per change at info level with the acting identity and the object, never
// a token value. The REST handlers and the MCP tools both call it, so a change leaves the same line by either path.
func auditLog(ctx context.Context, action string, attrs ...any) {
	actor, kind := "anonymous", ""
	if id, ok := identityFromContext(ctx); ok {
		actor, kind = id.Username, id.Kind
	}
	args := append([]any{"action", action, "actor", actor, "actorKind", kind}, attrs...)
	slog.Info("audit", args...)
}
