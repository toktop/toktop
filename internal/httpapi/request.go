package httpapi

import (
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
