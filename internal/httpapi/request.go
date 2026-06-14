package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func readBodyCapped(w http.ResponseWriter, r *http.Request, maxBytes int64, readErrorCode string) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err == nil {
		return body, true
	}
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("payload exceeds %d bytes", maxBytes))
		return nil, false
	}
	writeError(w, http.StatusBadRequest, readErrorCode, err.Error())
	return nil, false
}

// decodeJSONBody reads a capped request body and unmarshals it into v. An empty
// body leaves v at its zero value (every field optional), so a parameterless
// control POST still works. Writes the matching HTTP error and returns false on
// an over-size, read, or malformed-JSON body. Shared by every control route that
// takes a small JSON body so they read params the same way (body, not query).
func decodeJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) bool {
	defer r.Body.Close()
	body, ok := readBodyCapped(w, r, maxBytes, "bad_body")
	if !ok {
		return false
	}
	if len(body) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	return true
}
