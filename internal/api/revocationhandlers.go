package api

import (
	"net/http"
	"time"
)

// revokeRequest is POST /api/admin/revocations' body: exactly one of tokenId (a token's id, printed by
// `ego auth mint-token`) or subject (a token label or GitHub login: everything issued to it up to now).
type revokeRequest struct {
	TokenID string `json:"tokenId,omitempty"`
	// Expiry is when the revoked token would have expired (unix seconds), after which the entry is dropped on
	// restart. Omitted, the entry is kept for ten years.
	Expiry  int64  `json:"expiry,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// adminRevocationsHandler implements GET (list) and POST (revoke) on /api/admin/revocations. Admin only.
func adminRevocationsHandler(revoked *revocations) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		if revoked == nil {
			writeEvaluateError(w, http.StatusServiceUnavailable, "token revocation requires --session-secret to be enabled")
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"data": revoked.List(), "error": nil})
		case http.MethodPost:
			var req revokeRequest
			if err := decodeJSON(w, r, &req); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
				return
			}
			if (req.TokenID == "") == (req.Subject == "") {
				writeEvaluateError(w, http.StatusBadRequest, "give exactly one of tokenId or subject")
				return
			}
			var err error
			if req.TokenID != "" {
				expiry := req.Expiry
				if expiry == 0 {
					expiry = time.Now().AddDate(10, 0, 0).Unix()
				}
				err = revoked.RevokeToken(req.TokenID, expiry)
			} else {
				err = revoked.RevokeSubject(req.Subject, time.Now())
			}
			if err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to record the revocation: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": revoked.List(), "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
