package api

import (
	"net/http"
)

// senderRequest is POST /api/admin/senders' body.
type senderRequest struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type senderTokenRequest struct {
	Label string `json:"label,omitempty"`
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
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}, "error": nil})
	}
}

// adminSenderTokensHandler implements GET (list) and POST (mint) on /api/admin/senders/{id}/tokens. The minted
// token is in the POST response only. Admin only.
func adminSenderTokensHandler(senders *SenderStore) http.HandlerFunc {
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
			token, meta, err := senders.NewToken(id, req.Label)
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
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"revoked": true}, "error": nil})
	}
}
