package api

import (
	"net/http"

	"github.com/triageagent-dev/ego/internal/adk"
)

// evalSetsHandler implements both GET (list) and POST (save) on
// /api/evalsets, matching this codebase's one-handler-per-path convention
// (method dispatch happens inside, same as every other handler here)
// rather than Go 1.22's method-prefixed mux patterns.
func evalSetsHandler(store *SQLiteStore) http.HandlerFunc {
	list := listEvalSetsHandler(store)
	save := saveEvalSetHandler(store)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			save(w, r)
			return
		}
		list(w, r)
	}
}

// listEvalSetsHandler implements GET /api/evalsets.
func listEvalSetsHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyScoped(w, r) {
			return
		}
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		summaries, err := store.ListEvalSets()
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to list eval sets: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": summaries, "error": nil})
	}
}

// saveEvalSetHandler implements POST /api/evalsets: the request body is a
// full adk.EvalSet JSON document (the same shape the EvalSet Builder's
// downloadEvalSet already writes to disk - see ui/src/lib/evalset-builder.ts),
// upserted by eval_set_id so re-saving the same ID updates it in place.
func saveEvalSetHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !roleInfoOrOpen(r.Context()).CanWrite() {
			writeEvaluateError(w, http.StatusForbidden, "this role cannot save eval sets")
			return
		}
		var evalSet adk.EvalSet
		if err := decodeJSON(w, r, &evalSet); err != nil {
			writeEvaluateError(w, http.StatusBadRequest, "invalid eval set JSON: "+err.Error())
			return
		}
		if evalSet.EvalSetID == "" {
			writeEvaluateError(w, http.StatusBadRequest, "eval_set_id is required")
			return
		}
		if len(evalSet.EvalCases) == 0 {
			writeEvaluateError(w, http.StatusBadRequest, "at least one eval case is required")
			return
		}
		summary, err := store.SaveEvalSet(evalSet)
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to save eval set: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": summary, "error": nil})
	}
}

// getEvalSetHandler implements GET /api/evalsets/{id}.
func getEvalSetHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyScoped(w, r) {
			return
		}
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		evalSet, ok, err := store.GetEvalSet(r.PathValue("id"))
		if err != nil {
			writeEvaluateError(w, http.StatusInternalServerError, "failed to fetch eval set: "+err.Error())
			return
		}
		if !ok {
			writeEvaluateError(w, http.StatusNotFound, "eval set not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": evalSet, "error": nil})
	}
}
