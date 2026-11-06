package api

import (
	"errors"
	"fmt"
	"net/http"
)

// senderRequest is POST /api/admin/senders' body.
type senderRequest struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type senderTokenRequest struct {
	Label string `json:"label,omitempty"`
	// Tenant binds the token to one tenant of the sender (see SenderToken.Tenant); TenantName is what that tenant is
	// called in the UI.
	Tenant     string `json:"tenant,omitempty"`
	TenantName string `json:"tenantName,omitempty"`
	// MCP lets the token read over /mcp as well as send (see SenderToken.MCP). Off unless asked for.
	MCP bool `json:"mcp,omitempty"`
}

// adminSendersHandler implements GET (list) and POST (create) on /api/admin/senders. Admin only.
func adminSendersHandler(senders *SenderStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"data": senders.List(), "error": nil})
		case http.MethodPost:
			var req senderRequest
			if err := decodeJSON(w, r, &req); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
				return
			}
			t, err := senders.Create(req.ID, req.Name)
			if err != nil {
				writeEvaluateError(w, http.StatusBadRequest, err.Error())
				return
			}
			auditLog(r.Context(), "sender.create", "sender", req.ID)
			writeJSON(w, http.StatusOK, map[string]any{"data": t, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// adminSenderByIDHandler implements DELETE /api/admin/senders/{id}. Admin only.
func adminSenderByIDHandler(senders *SenderStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ok, err := senders.Delete(r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to delete sender: "+err.Error())
			return
		}
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "sender not found")
			return
		}
		auditLog(r.Context(), "sender.delete", "sender", r.PathValue("id"))
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}, "error": nil})
	}
}

// adminSenderTokensHandler implements GET (list) and POST (mint) on /api/admin/senders/{id}/tokens. The minted
// token is in the POST response only. Admin only.
func adminSenderTokensHandler(senders *SenderStore, names *TenantNames) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		id := r.PathValue("id")
		if !senders.Exists(id) {
			writeEvaluateError(w, http.StatusNotFound, "sender not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"data": senders.Tokens(id), "error": nil})
		case http.MethodPost:
			var req senderTokenRequest
			if r.ContentLength != 0 {
				if err := decodeJSON(w, r, &req); err != nil {
					writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
					return
				}
			}
			token, meta, status, err := mintSenderToken(senders, names, id, req)
			if err != nil {
				writeEvaluateError(w, status, err.Error())
				return
			}
			auditLog(r.Context(), "token.mint", "sender", id, "tokenId", meta.ID, "tenant", meta.Tenant, "mcp", meta.MCP)
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"token": token, "meta": meta}, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// mintSenderToken is the one place an ingest token is minted, for POST /api/admin/senders/{id}/tokens and for the
// mint_sender_token tool. The tenant's display name is checked and stored before the token exists, so a bad one
// does not leave a token behind. The status is the HTTP status of the error, when there is one.
func mintSenderToken(senders *SenderStore, names *TenantNames, sender string, req senderTokenRequest) (string, SenderToken, int, error) {
	if req.TenantName != "" && req.Tenant == "" {
		return "", SenderToken{}, http.StatusBadRequest, errors.New("tenantName needs a tenant")
	}
	if req.Tenant != "" {
		if err := ValidTenantLabel(req.Tenant); err != nil {
			return "", SenderToken{}, http.StatusBadRequest, err
		}
	}
	if req.TenantName != "" {
		if names == nil {
			return "", SenderToken{}, http.StatusBadRequest, errors.New("tenant names are not available on this server")
		}
		if err := names.Set(tenantKey(sender, req.Tenant), req.TenantName); err != nil {
			return "", SenderToken{}, http.StatusBadRequest, err
		}
	}
	token, meta, err := senders.NewToken(sender, req.Label, req.Tenant, req.MCP)
	if err != nil {
		return "", SenderToken{}, http.StatusInternalServerError, fmt.Errorf("failed to mint token: %w", err)
	}
	return token, meta, http.StatusOK, nil
}

// adminSenderTokenByIDHandler implements DELETE /api/admin/senders/{id}/tokens/{tokenId}. Admin only.
func adminSenderTokenByIDHandler(senders *SenderStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ok, err := senders.RevokeToken(r.PathValue("id"), r.PathValue("tokenId"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to revoke token: "+err.Error())
			return
		}
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "token not found")
			return
		}
		auditLog(r.Context(), "token.revoke", "sender", r.PathValue("id"), "tokenId", r.PathValue("tokenId"))
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"revoked": true}, "error": nil})
	}
}
