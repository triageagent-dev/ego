package api

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"strings"
)

// modelRate is the price of a model in USD per 1,000,000 tokens. The cache factors are the share of the input rate a
// cache read and a cache write cost; absent, the cached part of an input is priced at the plain rate.
type modelRate struct {
	InputPerMillion  float64  `json:"inputPerMillion"`
	OutputPerMillion float64  `json:"outputPerMillion"`
	CacheReadFactor  *float64 `json:"cacheReadFactor,omitempty"`
	CacheWriteFactor *float64 `json:"cacheWriteFactor,omitempty"`
}

func validRate(f float64) bool { return f >= 0 && !math.IsNaN(f) && !math.IsInf(f, 0) }

// validate refuses a price that is negative or not a number, so a typo is found when it is saved, not as a wrong cost
// on a screen.
func (r modelRate) validate() error {
	ok := validRate(r.InputPerMillion) && validRate(r.OutputPerMillion)
	for _, f := range []*float64{r.CacheReadFactor, r.CacheWriteFactor} {
		ok = ok && (f == nil || validRate(*f))
	}
	if !ok {
		return fmt.Errorf("a price must be a number of 0 or more")
	}
	return nil
}

func priceKey(model string) string { return strings.ToLower(strings.TrimSpace(model)) }

// ModelPrices returns the prices an admin set, by lower-cased model id.
func (s *SQLiteStore) ModelPrices() (map[string]modelRate, error) {
	rows, err := s.db.Query(`SELECT model, input_per_million, output_per_million, cache_read_factor, cache_write_factor FROM model_prices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]modelRate{}
	for rows.Next() {
		var m string
		var r modelRate
		var cr, cw sql.NullFloat64
		if err := rows.Scan(&m, &r.InputPerMillion, &r.OutputPerMillion, &cr, &cw); err != nil {
			return nil, err
		}
		if cr.Valid {
			r.CacheReadFactor = &cr.Float64
		}
		if cw.Valid {
			r.CacheWriteFactor = &cw.Float64
		}
		out[m] = r
	}
	return out, rows.Err()
}

// SetModelPrice saves the price of a model, replacing its earlier one.
func (s *SQLiteStore) SetModelPrice(model string, r modelRate) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO model_prices (model, input_per_million, output_per_million, cache_read_factor, cache_write_factor) VALUES (?, ?, ?, ?, ?)`,
		priceKey(model), r.InputPerMillion, r.OutputPerMillion, r.CacheReadFactor, r.CacheWriteFactor)
	return err
}

// DeleteModelPrice drops a model's saved price, so the built-in list prices it again.
func (s *SQLiteStore) DeleteModelPrice(model string) error {
	_, err := s.db.Exec(`DELETE FROM model_prices WHERE model = ?`, priceKey(model))
	return err
}

// usagePricingHandler implements GET /api/usage/pricing: the prices an admin set, which the UI applies over its
// built-in list. Without a session database there are none.
func usagePricingHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rates := map[string]modelRate{}
		if store != nil {
			var err error
			if rates, err = store.ModelPrices(); err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to read the prices: "+err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"rates": rates}, "error": nil})
	}
}

// adminPricingHandler implements PUT and DELETE /api/admin/pricing/{model}: save or drop one model's price (the body
// of a PUT is a modelRate). The model id may hold slashes. Admin only; it needs the session database.
func adminPricingHandler(store *SQLiteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !roleInfoOrOpen(r.Context()).IsAdmin {
			writeRoleForbidden(w)
			return
		}
		model := priceKey(r.PathValue("model"))
		if model == "" {
			writeEvaluateError(w, http.StatusBadRequest, "name the model: /api/admin/pricing/<model id>")
			return
		}
		if store == nil {
			writeEvaluateError(w, http.StatusServiceUnavailable, "prices are kept in the session database (start the server with --session-db)")
			return
		}
		switch r.Method {
		case http.MethodPut:
			var rate modelRate
			if err := decodeJSONStrict(w, r, &rate); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, "invalid price: "+err.Error())
				return
			}
			if err := rate.validate(); err != nil {
				writeEvaluateError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := store.SetModelPrice(model, rate); err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to save the price: "+err.Error())
				return
			}
		case http.MethodDelete:
			if err := store.DeleteModelPrice(model); err != nil {
				writeEvaluateError(w, http.StatusInternalServerError, "failed to drop the price: "+err.Error())
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"model": model}, "error": nil})
	}
}
