package api

import (
	"path/filepath"
	"strings"
	"testing"
)

// The start asks whether any ledger row still lacks its cache split or its kind. Both questions must be answered from
// an index over those rows, not by reading the whole ledger.
func TestStartChecksOfTheLedgerUseAnIndex(t *testing.T) {
	archive, err := NewSQLiteStore(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for query, index := range map[string]string{
		`SELECT 1 FROM llm_calls WHERE cache_read_tokens IS NULL LIMIT 1`: "idx_llm_calls_cache_unknown",
		`SELECT DISTINCT session_id FROM llm_calls WHERE kind = ''`:       "idx_llm_calls_kindless",
	} {
		rows, err := archive.db.Query("EXPLAIN QUERY PLAN " + query)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		if !strings.Contains(strings.Join(plan, " | "), index) {
			t.Errorf("%s: plan %v does not use %s", query, plan, index)
		}
	}
}
