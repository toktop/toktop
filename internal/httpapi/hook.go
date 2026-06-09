package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"toktop.unceas.dev/internal/paths"
	"toktop.unceas.dev/internal/redact"
	"toktop.unceas.dev/internal/trace"
)

// runSpoolWriter owns the hook spool file: it is the only goroutine that opens,
// rotates, redacts into, and writes the spool, so the request path never blocks
// on the full-body redact scan or disk I/O. It exits when spoolCh is closed
// (stopSpooler), closing spoolDone so Close can wait for the drain.
func (s *Server) runSpoolWriter() {
	defer close(s.spoolDone)
	for body := range s.spoolCh {
		if err := s.appendSpoolLine(body); err != nil {
			s.logger.Warn("write hook spool failed", "err", err)
		}
	}
}

// enqueueSpool hands a raw hook body to runSpoolWriter without blocking the
// request. On overflow the body is dropped from the audit spool (warned) — the
// live-event projection over SSE is unaffected, since Emit redacts and publishes
// it independently. Mirrors enqueueEventPersist's lossy-by-design contract.
func (s *Server) enqueueSpool(body []byte) {
	select {
	case s.spoolCh <- body:
	default:
		s.logger.Warn("hook spool queue full; payload not written to audit spool",
			"bytes", len(body))
	}
}

// stopSpooler closes spoolCh and waits for runSpoolWriter to drain. Idempotent
// (Close runs on several exit paths).
func (s *Server) stopSpooler() {
	s.spoolOnce.Do(func() {
		if s.spoolCh != nil {
			close(s.spoolCh)
			<-s.spoolDone
		}
	})
}

// appendSpoolLine writes one redacted hook body to the daily spool file, opening
// or rotating it as needed. Called only from runSpoolWriter, so spoolFile/
// spoolDate need no lock.
func (s *Server) appendSpoolLine(body []byte) error {
	dir, err := paths.DataDir()
	if err != nil {
		return err
	}
	spoolDir := filepath.Join(dir, "hooks", "spool")
	date := time.Now().UTC().Format("2006-01-02")
	if s.spoolFile == nil || s.spoolDate != date {
		if s.spoolFile != nil {
			_ = s.spoolFile.Close()
			s.spoolFile = nil
		}
		if err := os.MkdirAll(spoolDir, 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(spoolDir, date+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		s.spoolFile = f
		s.spoolDate = date
	}
	// Redact before the body hits disk: the spool is persisted raw hook payloads
	// that can echo secrets, mirroring the redaction Emit applies to the
	// live-event projection. redact.Apply is a no-op on secret-free text and
	// only rewrites secret substrings, so JSON string framing is preserved.
	line := redact.Apply(string(compactSpoolLine(body))).Redacted
	if _, err := s.spoolFile.WriteString(line); err != nil {
		return err
	}
	if _, err := s.spoolFile.Write([]byte{'\n'}); err != nil {
		return err
	}
	return nil
}

// compactSpoolLine collapses a hook body into a single physical line so the
// spool file keeps its one-object-per-line (.jsonl) framing. JSON permits
// literal newlines between tokens, so a pretty-printed payload would otherwise
// span multiple physical lines and corrupt the line framing. When the body is
// not valid JSON it falls back to escaping embedded CR/LF.
func compactSpoolLine(body []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err == nil {
		return buf.Bytes()
	}
	return bytes.ReplaceAll(bytes.ReplaceAll(body, []byte{'\r'}, []byte(`\r`)), []byte{'\n'}, []byte(`\n`))
}

func (s *Server) handleHooksIntake(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookIntakeBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("payload exceeds %d bytes", maxHookIntakeBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "read_body_failed", err.Error())
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty_body", "no payload")
		return
	}
	// Spool the raw body for audit off the request path: redaction (a full-body
	// gitleaks scan) and disk I/O happen in runSpoolWriter, so the live
	// hook→status→SSE path below isn't gated on them. Both enqueueSpool's consumer
	// and liveEventFromHook only read body; the request goroutine never mutates it
	// after the ReadAll above, so the concurrent reads are race-free.
	s.enqueueSpool(body)
	providerHint := firstNonEmpty(r.URL.Query().Get("provider"), r.URL.Query().Get("app"), r.URL.Query().Get("source"))
	ev := liveEventFromHook(body, providerHint)
	ev.SizeBytes = len(body)
	stored, err := s.Emit(r.Context(), ev)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "emit_live_event_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "bytes": len(body), "event_id": stored.EventID})
}

func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func liveEventFromHook(body []byte, providerHint string) LiveEvent {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		// No hardcoded provider default: installed hooks always set ?provider=<name>
		// (appendHookQuery), so providerHint is normally present. When it isn't (a
		// hand-rolled request with a non-JSON body), leave Provider empty and let
		// normalizeLiveEvent/the registry resolve it; Status stays unknown.
		return LiveEvent{
			Type:     "hook.intake",
			Provider: providerHint,
		}
	}
	f := collectHookFields(payload)
	evType := "hook.intake"
	if f.hookName != "" {
		evType = "hook." + normalizeEventName(f.hookName)
	}
	// Provider/SourceID/Status are intentionally left for normalizeLiveEvent (the
	// single derivation site): it resolves the provider via the registry, computes
	// SourceID, and maps RawEventName→status through the provider's HookInstaller
	// seam (falling back to the liveStatusForType heuristic).
	return LiveEvent{
		Type:              evType,
		RawEventName:      f.hookName,
		At:                f.at,
		Provider:          firstNonEmpty(providerHint, f.provider),
		SessionID:         f.sessionID,
		ExternalSessionID: f.externalSessionID,
		ProjectID:         f.projectID,
		ProjectName:       f.projectName,
		ProjectPath:       f.projectPath,
		TranscriptPath:    f.transcriptPath,
		Reason:            f.reason,
	}
}

// hookFields holds every value liveEventFromHook extracts from the parsed hook
// body. They are filled in ONE pass over the JSON tree (collectHookFields)
// instead of one independent depth-first walk per field.
type hookFields struct {
	hookName, provider, sessionID, externalSessionID    string
	projectID, projectName, projectPath, transcriptPath string
	reason                                              string
	at                                                  time.Time
}

type hookFieldID int

const (
	_ hookFieldID = iota // 0 is "no field"; real ids start at 1
	fldHookName
	fldProvider
	fldSessionID
	fldExternalSessionID
	fldProjectID
	fldProjectName
	fldProjectPath
	fldTranscriptPath
	fldReason
	fldAt
)

// hookFieldAliases maps each normalized JSON key to the field it fills. It is the
// same per-field alias lists liveEventFromHook used to pass to the firstJSON*
// walks, flattened into one table so a single pass can route each key. The alias
// sets are disjoint, so no key maps to two fields.
var hookFieldAliases = func() map[string]hookFieldID {
	groups := map[hookFieldID][]string{
		fldHookName:          {"hook_event_name", "hookEventName", "event", "event_name", "type"},
		fldProvider:          {"provider", "app", "source"},
		fldSessionID:         {"session_id", "sessionId", "sessionID", "session"},
		fldExternalSessionID: {"external_session_id", "externalSessionId"},
		fldProjectID:         {"project_id", "projectId"},
		fldProjectName:       {"project_name", "projectName", "project"},
		fldProjectPath:       {"cwd", "project_path", "projectPath", "workspace"},
		fldTranscriptPath:    {"transcript_path", "transcriptPath", "transcript"},
		fldReason:            {"reason", "message"},
		fldAt:                {"timestamp", "time", "at", "occurred_at", "occurredAt", "created_at", "createdAt", "started_at", "startedAt"},
	}
	m := make(map[string]hookFieldID)
	for id, keys := range groups {
		for _, key := range keys {
			m[normalizeJSONKey(key)] = id
		}
	}
	return m
}()

func collectHookFields(payload any) hookFields {
	var f hookFields
	// Node budget bounds this single combined walk so a 16 MiB intake body
	// (maxHookIntakeBytes) cannot pin the request goroutine. Depth stays ≤5, so
	// memory is O(depth) on the stack regardless of body width — raising the
	// ceiling only widens reach, not the allocation profile. 256 suffices when
	// the fields sit at the top level (the real Claude Code / Codex hook shape,
	// matched before any descent), but a forwarder that wraps the hook in an
	// envelope or nests it behind a large sibling array can push them past a
	// 256-node frontier; 4096 covers those bodies while still costing a few µs.
	walkHookFields(payload, 0, new(4096), &f)
	return f
}

// walkHookFields is the single depth-first pass that fills f. It preserves the
// firstJSON* walks' behavior: descend maps and array elements to depth 5 under a
// shared node budget, match a key directly at the current level before recursing,
// and keep the first non-empty value found for each field.
func walkHookFields(value any, depth int, budget *int, f *hookFields) {
	if depth > 5 || *budget <= 0 {
		return
	}
	*budget--
	switch typed := value.(type) {
	case map[string]any:
		// Visit keys in a deterministic (sorted) order, not Go's randomized map
		// order: when two distinct aliases of one field co-occur in an object (e.g.
		// session_id + session), "keep the first non-empty" must pick the same one
		// every run, or the resolved SessionID/Reason flips across daemon restarts.
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			f.assign(normalizeJSONKey(key), typed[key])
		}
		for _, key := range keys {
			walkHookFields(typed[key], depth+1, budget, f)
			if *budget <= 0 {
				return
			}
		}
	case []any:
		for _, child := range typed {
			walkHookFields(child, depth+1, budget, f)
			if *budget <= 0 {
				return
			}
		}
	}
}

func (f *hookFields) assign(normKey string, child any) {
	id, ok := hookFieldAliases[normKey]
	if !ok {
		return
	}
	if id == fldAt {
		if f.at.IsZero() {
			if t, ok := timeFromAny(child); ok {
				f.at = t
			}
		}
		return
	}
	if p := f.stringField(id); p != nil && *p == "" {
		if text := stringFromAny(child); text != "" {
			*p = text
		}
	}
}

func (f *hookFields) stringField(id hookFieldID) *string {
	switch id {
	case fldHookName:
		return &f.hookName
	case fldProvider:
		return &f.provider
	case fldSessionID:
		return &f.sessionID
	case fldExternalSessionID:
		return &f.externalSessionID
	case fldProjectID:
		return &f.projectID
	case fldProjectName:
		return &f.projectName
	case fldProjectPath:
		return &f.projectPath
	case fldTranscriptPath:
		return &f.transcriptPath
	case fldReason:
		return &f.reason
	default:
		return nil
	}
}

// liveStatusForType is the provider-AGNOSTIC fallback, reached only when a
// provider's HookEventStatus did not map the event (unknown provider, non-hook
// event, or a legacy event with no RawEventName). It must hold only GENERIC
// substrings — never a provider's exact event-name token: each provider's
// vocabulary (claude StopFailure, codex PermissionRequest, …) is owned,
// authoritatively, by its HookEventStatus. This is only a best-effort guess, not
// a guarantee — an event that should carry a status belongs in that map, not
// here. The generic tokens are chosen so today's mapped events would still land
// in the right bucket if they fell through (e.g. "stopfailure" hits "fail"
// before the "stop" success case; "userpromptsubmit" hits "submit"), but they
// stay deliberately conservative: a bare "prompt" event (which may be *awaiting*
// input) is left Unknown rather than asserted Active.
func liveStatusForType(eventType string) string {
	name := strings.ToLower(eventType)
	switch {
	case strings.Contains(name, "approval") ||
		strings.Contains(name, "confirm") ||
		strings.Contains(name, "permission") ||
		strings.Contains(name, "waiting") ||
		strings.Contains(name, "needs_input"):
		return trace.StatusAwaitingConfirmation
	case strings.Contains(name, "fail") ||
		strings.Contains(name, "error") ||
		strings.Contains(name, "denied"):
		return trace.StatusFailed
	case strings.Contains(name, "success") ||
		strings.Contains(name, "complete") ||
		strings.Contains(name, "stop"):
		return trace.StatusSuccess
	case strings.Contains(name, "start") ||
		strings.Contains(name, "submit") ||
		strings.Contains(name, "tool") ||
		strings.Contains(name, "compact") ||
		strings.Contains(name, "running") ||
		strings.Contains(name, "active"):
		return trace.StatusActive
	default:
		return trace.StatusUnknown
	}
}

func normalizeEventName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "_", "-", "_", ".", "_", "/", "_")
	return replacer.Replace(value)
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func timeFromAny(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case string:
		s := strings.TrimSpace(typed)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), true
			}
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return epochToTime(n), true
		}
	case float64:
		return epochToTime(typed), true
	}
	return time.Time{}, false
}

func epochToTime(n float64) time.Time {
	switch {
	case n >= 1e15:
		return time.Unix(0, int64(n)*int64(time.Microsecond)).UTC()
	case n >= 1e12:
		return time.Unix(0, int64(n)*int64(time.Millisecond)).UTC()
	default:
		sec := int64(n)
		return time.Unix(sec, int64((n-float64(sec))*1e9)).UTC()
	}
}

func normalizeJSONKey(value string) string {
	value = strings.ToLower(value)
	replacer := strings.NewReplacer("_", "", "-", "")
	return replacer.Replace(value)
}
