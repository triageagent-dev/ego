package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// REST for rubric sets: GET /api/rubricsets?kind=, POST /api/rubricsets (upsert by id), GET and DELETE
// /api/rubricsets/{id}. Reading is open to any role that may see eval sets; writing needs a role that can evaluate.

var rubricSetIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)

func newRubricSetID() string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("rubrics-%d", time.Now().UnixNano())
	}
	return "rubrics-" + hex.EncodeToString(b)
}

func validRubricKind(k string) bool { return k == RubricKindFinalResponse || k == RubricKindToolUse }

// cleanRubricSet trims and checks a set from a request body; the error is a message for a 400.
func cleanRubricSet(in rubricSetDTO) (rubricSetDTO, string) {
	in.RubricSetID, in.Name, in.Description = strings.TrimSpace(in.RubricSetID), strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	switch {
	case in.Name == "":
		return in, "name is required"
	case utf8.RuneCountInString(in.Name) > maxRubricSetNameChars:
		return in, "name is too long"
	case utf8.RuneCountInString(in.Description) > maxRubricSetDescChars:
		return in, "description is too long"
	case !validRubricKind(in.Kind):
		return in, "kind is final_response or tool_use"
	case in.RubricSetID != "" && !rubricSetIDRe.MatchString(in.RubricSetID):
		return in, "rubricSetId is lowercase letters, digits, dot, dash and underscore, at most 80 characters"
	}
	rubrics := make([]string, 0, len(in.Rubrics))
	for _, r := range in.Rubrics {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		if utf8.RuneCountInString(r) > maxRubricChars {
			return in, "a rubric is at most 500 characters"
		}
		rubrics = append(rubrics, r)
	}
	if len(rubrics) == 0 {
		return in, "at least one rubric is required"
	}
	if len(rubrics) > maxRubricsPerSet {
		return in, "a set holds at most 20 rubrics"
	}
	in.Rubrics = rubrics
	return in, ""
}

// rubricSetsHandler implements GET (list) and POST (save) on /api/rubricsets.
func rubricSetsHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyScoped(w, r) {
			return
		}
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
			kind := r.URL.Query().Get("kind")
			if kind != "" && !validRubricKind(kind) {
				writeEvaluateError(w, http.StatusBadRequest, "kind is final_response or tool_use")
				return
			}
			sets, err := store.ListRubricSets(kind)
			if err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to list rubric sets: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": sets, "error": nil})
		case http.MethodPost:
			if !roleInfoOrOpen(r.Context()).CanWrite() {
				writeEvaluateError(w, http.StatusForbidden, "this role cannot save rubric sets")
				return
			}
			var in rubricSetDTO
			if err := decodeJSON(w, r, &in); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, "invalid rubric set JSON: "+err.Error())
				return
			}
			set, msg := cleanRubricSet(in)
			if msg != "" {
				writeEvaluateError(w, http.StatusBadRequest, msg)
				return
			}
			if set.RubricSetID == "" {
				set.RubricSetID = newRubricSetID()
			}
			saved, err := store.SaveRubricSet(set)
			if err == errTooManyRubricSets {
				writeEvaluateError(w, http.StatusConflict, err.Error())
				return
			}
			if err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to save rubric set: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": saved, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// rubricSetHandler implements GET and DELETE on /api/rubricsets/{id}.
func rubricSetHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyScoped(w, r) {
			return
		}
		if store == nil {
			writeStorageUnavailable(w)
			return
		}
		id := r.PathValue("id")
		switch r.Method {
		case http.MethodGet:
			set, ok, err := store.GetRubricSet(id)
			if err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to fetch rubric set: "+err.Error())
				return
			}
			if !ok {
				writeEvaluateError(w, http.StatusNotFound, "rubric set not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": set, "error": nil})
		case http.MethodDelete:
			if !roleInfoOrOpen(r.Context()).CanWrite() {
				writeEvaluateError(w, http.StatusForbidden, "this role cannot delete rubric sets")
				return
			}
			ok, err := store.DeleteRubricSet(id)
			if err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to delete rubric set: "+err.Error())
				return
			}
			if !ok {
				writeEvaluateError(w, http.StatusNotFound, "rubric set not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"deleted": true}, "error": nil})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
