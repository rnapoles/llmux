// Package httpx contains tiny HTTP helpers shared by dialects.
package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

// Flush flushes w if the underlying writer supports it.
func Flush(w http.ResponseWriter) {
	_ = http.NewResponseController(w).Flush()
}
