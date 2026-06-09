package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type event struct {
	ID   string
	Type string
	Data any
	// dataJSON, when non-nil, is the pre-marshaled JSON of Data. The live emit
	// path fills it once (in Emit) so a fan-out to N subscribers costs
	// N memcpys instead of N JSON encodes of the identical payload. It is nil
	// for replay and control frames (hello/ping/resync), where writeSSE marshals
	// Data on demand.
	dataJSON []byte
}

func writeSSE(w io.Writer, flusher http.Flusher, ev event) error {
	payload := ev.dataJSON
	if payload == nil {
		var err error
		payload, err = json.Marshal(ev.Data)
		if err != nil {
			return err
		}
	}
	if ev.ID != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", sanitizeSSEField(ev.ID)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sanitizeSSEField(ev.Type), payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// sanitizeSSEField strips control characters from a single-line SSE field value
// (id/event) so a caller-controlled value cannot inject `\n`/`\r` and forge
// additional SSE frames. The transport layer enforces this regardless of how
// the caller built ev.Type/ev.ID.
func sanitizeSSEField(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 { // control chars, including `\n` (0x0A) and `\r` (0x0D)
			return '_'
		}
		return r
	}, value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}
