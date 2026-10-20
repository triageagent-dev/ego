package api

import (
	"encoding/json"
	"net/http"
)

// roleshandlers.go implements GET/POST /api/admin/roles and
// DELETE /api/admin/roles/{id}: the REST surface an admin uses to assign
// Roles to GitHub usernames/teams (see roles.go). Every handler here
// requires RoleInfo.IsAdmin - checked via roleInfoOrOpen, so with RBAC
// unconfigured (no --session-secret) these behave exactly like every
// other route: open, no role to check.

// roleBindingRequest is POST /api/admin/roles' request body. Setting ID
// updates that existing binding in place (SubjectType/Subject/Role/Agents
// all replaced wholesale, matching RoleStore.Upsert); omitting it creates
// a new one.
type roleBindingRequest struct {
	ID          string   `json:"id,omitempty"`
	SubjectType string   `json:"subjectType"`
	Subject     string   `json:"subject"`
	Role        string   `json:"role"`
	Agents      []string `json:"agents,omitempty"`
}

// writeRoleForbidden matches writeEvaluateError's envelope shape - kept
// as its own helper since the message is the same across every handler
// in this file.
func writeRoleForbidden(w http.ResponseWriter) {
	writeEvaluateError(w, http.StatusForbidden, "admin role required")
}

func writeRolesUnavailable(w http.ResponseWriter) {
	writeEvaluateError(w, http.StatusServiceUnavailable, "role management requires --session-secret (and GitHub OAuth) to be enabled")
}

// adminRolesHandler implements GET (list) and POST (create/update) on
// /api/admin/roles, matching this codebase's one-handler-per-path
// convention (see evalsets.go's evalSetsHandler doc comment).
func adminRolesHandler(roleStore *RoleStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if roleStore == nil {
			writeRolesUnavailable(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"data": roleStore.List(), "error": nil})
		case http.MethodPost:
			var req roleBindingRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
				return
			}
			role, err := ParseRole(req.Role)
			if err != nil {
				writeEvaluateError(w, http.StatusBadRequest, err.Error())
				return
			}
			binding, err := roleStore.Upsert(RoleBinding{
				ID:          req.ID,
				SubjectType: req.SubjectType,
				Subject:     req.Subject,
				Role:        role,
				Agents:      req.Agents,
			})
			if err != nil {
				writeEvaluateError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": binding, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// adminRoleByIDHandler implements DELETE /api/admin/roles/{id}.
func adminRoleByIDHandler(roleStore *RoleStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if roleStore == nil {
			writeRolesUnavailable(w)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ok, err := roleStore.Delete(r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to delete role binding: "+err.Error())
			return
		}
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "role binding not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}, "error": nil})
	}
}
