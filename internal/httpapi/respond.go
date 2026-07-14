package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON strictly decodes the request body into dst. Unknown fields are
// rejected (fail closed on typos). Returns an error the caller maps to 400.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// pathInt64 parses a chi URL param as int64.
func pathInt64(r *http.Request, key string) (int64, error) {
	v := chi.URLParam(r, key)
	if v == "" {
		return 0, errors.New("missing path parameter: " + key)
	}
	return strconv.ParseInt(v, 10, 64)
}
