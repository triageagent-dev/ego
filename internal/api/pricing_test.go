package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func pricingStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func pricingCall(t *testing.T, s *SQLiteStore, admin bool, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/usage/pricing", usagePricingHandler(s))
	mux.HandleFunc("/api/admin/pricing/{model...}", adminPricingHandler(s))
	info := RoleInfo{Role: RoleMember}
	if admin {
		info = RoleInfo{Role: RoleAdmin, IsAdmin: true}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(asRole(info))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestModelPriceIsOneRowAndShowsAtOnce(t *testing.T) {
	s := pricingStore(t)
	if rec := pricingCall(t, s, true, http.MethodPut, "/api/admin/pricing/Gemini-Decision-1", `{"inputPerMillion":0.1,"outputPerMillion":0}`); rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	// Ids with a slash (typesafe/jev-1.13) are one model id.
	if rec := pricingCall(t, s, true, http.MethodPut, "/api/admin/pricing/typesafe/jev-1.13", `{"inputPerMillion":0.042,"outputPerMillion":0,"cacheReadFactor":0.1}`); rec.Code != 200 {
		t.Fatalf("put slash id: %d %s", rec.Code, rec.Body)
	}
	body := pricingCall(t, s, false, http.MethodGet, "/api/usage/pricing", "").Body.String()
	if !strings.Contains(body, `"gemini-decision-1":{"inputPerMillion":0.1,"outputPerMillion":0}`) || !strings.Contains(body, `"typesafe/jev-1.13":{"inputPerMillion":0.042,"outputPerMillion":0,"cacheReadFactor":0.1}`) {
		t.Errorf("a signed-in member reads the prices, ids lower-cased: %s", body)
	}
	// One change in the table, as an operator would make it with sqlite, is the price.
	if _, err := s.db.Exec(`UPDATE model_prices SET input_per_million = 0.2 WHERE model = 'gemini-decision-1'`); err != nil {
		t.Fatal(err)
	}
	if body := pricingCall(t, s, false, http.MethodGet, "/api/usage/pricing", "").Body.String(); !strings.Contains(body, `"inputPerMillion":0.2`) {
		t.Errorf("an UPDATE shows on the next read: %s", body)
	}
	if rec := pricingCall(t, s, true, http.MethodDelete, "/api/admin/pricing/gemini-decision-1", ""); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if body := pricingCall(t, s, false, http.MethodGet, "/api/usage/pricing", "").Body.String(); strings.Contains(body, "gemini-decision-1") {
		t.Errorf("a dropped price is gone: %s", body)
	}
}

func TestPriceWritesAreAdminOnlyAndValidated(t *testing.T) {
	s := pricingStore(t)
	if rec := pricingCall(t, s, false, http.MethodPut, "/api/admin/pricing/m", `{"inputPerMillion":1,"outputPerMillion":1}`); rec.Code != http.StatusForbidden {
		t.Errorf("a member writing a price: %d", rec.Code)
	}
	for _, body := range []string{`{"inputPerMillion":-1,"outputPerMillion":0}`, `{"inputPerMillion":1,"outputPerMillion":1,"cacheReadFactor":-0.1}`, `{"inputPerMillion":1,"outputPerMillion":1,"x":1}`, `nope`} {
		if rec := pricingCall(t, s, true, http.MethodPut, "/api/admin/pricing/m", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}
	if rec := pricingCall(t, s, true, http.MethodPost, "/api/admin/pricing/m", `{}`); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestPricingWithoutADatabaseIsEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	usagePricingHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/api/usage/pricing", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"rates":{}`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
