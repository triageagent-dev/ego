package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/triageagent-dev/ego/internal/mcptools"
)

// REST twins of get_findings, list_corrections and register_correction. They answer in the tools' own shape (the
// types of internal/online), so the stdio client decodes them straight into it.

// maxCorrectionBody bounds a POST: what is at most 2000 characters, the rest are short labels.
const maxCorrectionBody = 64 << 10

// onlineFindingsHandler implements GET /api/online/findings?state=&kind=&tenant=&since=. An empty state lists
// every state; since is an RFC 3339 time or a duration such as 24h.
func onlineFindingsHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if e == nil {
			writeOnlineUnavailable(w)
			return
		}
		q := r.URL.Query()
		since, err := mcptools.ParseSince(q.Get("since"), e.now())
		if err != nil {
			writeEvaluateError(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := e.Findings(narrowedInfo(r), q.Get("kind"), q.Get("state"), since)
		if err != nil {
			writeStatusError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}

// onlineFindingsSweepHandler implements POST /api/online/findings/sweep: the REST twin of run_findings.
func onlineFindingsSweepHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out, err := e.SweepNow(r.Context(), roleInfoOrOpen(r.Context()))
		if err != nil {
			writeStatusError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}

// onlineFindingHandler implements GET /api/online/findings/{id}.
func onlineFindingHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out, err := e.Finding(roleInfoOrOpen(r.Context()), r.PathValue("id"))
		if err != nil {
			writeStatusError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
	}
}

// onlineCorrectionsHandler implements GET /api/online/corrections?state=&kind=&tenant= and POST (a write: a
// viewer gets 403, a tenant-limited member may post for its own tenants only).
func onlineCorrectionsHandler(e *OnlineEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			out, err := e.Corrections(narrowedInfo(r), q.Get("kind"), q.Get("state"))
			if err != nil {
				writeStatusError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": out, "error": nil})
		case http.MethodPost:
			var in mcptools.CorrectionInput
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCorrectionBody))
			if err := dec.Decode(&in); err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					writeEvaluateError(w, http.StatusRequestEntityTooLarge, "request body too large")
				} else if errors.Is(err, io.EOF) {
					writeEvaluateError(w, http.StatusBadRequest, "request body required")
				} else {
					writeEvaluateError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
				}
				return
			}
			out, err := e.RegisterCorrection(roleInfoOrOpen(r.Context()), in)
			if err != nil {
				writeStatusError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": out, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
