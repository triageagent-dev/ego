package api

import (
	"net/http"
)

// tenantRequest is POST /api/admin/tenants' body.
type tenantRequest struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type tenantTokenRequest struct {
	Label string `json:"label,omitempty"`
}

// adminTenantsHandler implements GET (list) and POST (create) on /api/admin/tenants. Admin only.
func adminTenantsHandler(tenants *TenantStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"data": tenants.List(), "error": nil})
		case http.MethodPost:
			var req tenantRequest
			if err := decodeJSON(w, r, &req); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
				return
			}
			t, err := tenants.Create(req.ID, req.Name)
			if err != nil {
				writeEvaluateError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": t, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// adminTenantByIDHandler implements DELETE /api/admin/tenants/{id}. Admin only.
func adminTenantByIDHandler(tenants *TenantStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ok, err := tenants.Delete(r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to delete tenant: "+err.Error())
			return
		}
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "tenant not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}, "error": nil})
	}
}

// adminTenantTokensHandler implements GET (list) and POST (mint) on /api/admin/tenants/{id}/tokens. The minted
// token is in the POST response only. Admin only.
func adminTenantTokensHandler(tenants *TenantStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		id := r.PathValue("id")
		if !tenants.Exists(id) {
			writeEvaluateError(w, http.StatusNotFound, "tenant not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"data": tenants.Tokens(id), "error": nil})
		case http.MethodPost:
			var req tenantTokenRequest
			if r.ContentLength != 0 {
				if err := decodeJSON(w, r, &req); err != nil {
					writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
					return
				}
			}
			token, meta, err := tenants.NewToken(id, req.Label)
			if err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to mint token: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"token": token, "meta": meta}, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// adminTenantTokenByIDHandler implements DELETE /api/admin/tenants/{id}/tokens/{tokenId}. Admin only.
func adminTenantTokenByIDHandler(tenants *TenantStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ok, err := tenants.RevokeToken(r.PathValue("id"), r.PathValue("tokenId"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to revoke token: "+err.Error())
			return
		}
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "token not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"revoked": true}, "error": nil})
	}
}
