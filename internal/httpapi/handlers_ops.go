package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"time"

	"toktop.unceas.dev/internal/config"
	"toktop.unceas.dev/internal/fsx"
	"toktop.unceas.dev/internal/ingest"
	"toktop.unceas.dev/internal/paths"
	"toktop.unceas.dev/internal/retention"
)

// pruneDryRun resolves the dry_run flag for a control prune route: the JSON body
// value wins (so a body is never silently ignored), then the query param (the
// CLI client sends it that way), defaulting to true — a prune dry-runs unless
// dry_run is explicitly disabled, so it never deletes by accident.
func pruneDryRun(body *bool, q url.Values) bool {
	if body != nil {
		return *body
	}
	return !isFalsy(q.Get("dry_run"))
}

func (s *Server) handleSources(w http.ResponseWriter, _ *http.Request) {
	type sourceRoot struct {
		Source string `json:"source"`
		Root   string `json:"root"`
		Exists bool   `json:"exists"`
	}
	rows := []sourceRoot{}
	// Prefer the live config snapshot (includes config.json roots) so this
	// matches GET /v1/config; fall back to plain discovery when no loader.
	snapshotRoots := s.rootsMap()
	for _, name := range ingest.SortedProviders() {
		roots := snapshotRoots[name]
		if len(roots) == 0 {
			roots = ingest.DiscoverRootPaths(name, nil)
		}
		if len(roots) == 0 {
			rows = append(rows, sourceRoot{Source: name})
			continue
		}
		for _, root := range roots {
			rows = append(rows, sourceRoot{Source: name, Root: root, Exists: fsx.DirExists(root)})
		}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	home, _ := paths.Home()
	configDir, _ := paths.ConfigDir()
	dataDir, _ := paths.DataDir()
	tokenPath := paths.APITokenPathUnder(home)
	_, tokenErr := os.Stat(tokenPath)
	writeJSON(w, http.StatusOK, map[string]any{
		"home_dir":       home,
		"config_dir":     configDir,
		"data_dir":       dataDir,
		"api_token_path": tokenPath,
		"api_token_set":  tokenErr == nil,
		"redact":         s.redactValue(),
		"roots":          s.rootsMap(),
		"settings":       s.configSettings(),
	})
}

// configSettings builds the settings block for GET /v1/config: value+source+editable
// for each key the web UI may display. addr is included read-only (editable:false)
// so the page can show the current listen address without being able to change it.
func (s *Server) configSettings() map[string]any {
	type entry struct {
		Value    string `json:"value"`
		Source   string `json:"source"`
		Editable bool   `json:"editable"`
	}
	if s.cfgLoader == nil {
		return map[string]any{}
	}
	snap := s.cfgLoader.Current()
	canonicalBool := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	canonicalInterval := func(d time.Duration) string {
		if d == 0 {
			return ""
		}
		return d.String()
	}
	keys := []string{"redact", "autostart", "idle_stop", "timezone", "interval", "addr"}
	values := map[string]string{
		"redact":    config.CanonicalRedact(snap.RedactPolicy),
		"autostart": canonicalBool(snap.Autostart),
		"idle_stop": canonicalBool(snap.IdleStop),
		"timezone":  snap.Timezone,
		"interval":  canonicalInterval(snap.Interval),
		"addr":      snap.Addr,
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = entry{
			Value:    values[k],
			Source:   s.cfgLoader.Source(k),
			Editable: config.RemoteSettable(k),
		}
	}
	return out
}

func (s *Server) handleConfigSet(w http.ResponseWriter, r *http.Request) {
	if s.cfgLoader == nil {
		writeError(w, http.StatusServiceUnavailable, "config_unavailable", "no config loader")
		return
	}
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if body.Key == "" {
		writeError(w, http.StatusBadRequest, "missing_key", "key is required")
		return
	}
	if !config.RemoteSettable(body.Key) {
		writeError(w, http.StatusForbidden, "key_not_remote_settable",
			"this key cannot be set over the API; change it with `toktop config set "+body.Key+" <value>`")
		return
	}
	if err := s.cfgLoader.Set(body.Key, body.Value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config_value", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": body.Key, "value": body.Value, "reloaded": true})
}

// handleConfigReload re-reads config.json into the live loader.
func (s *Server) handleConfigReload(w http.ResponseWriter, _ *http.Request) {
	if s.cfgLoader == nil {
		writeError(w, http.StatusServiceUnavailable, "config_unavailable", "no config loader")
		return
	}
	if err := s.cfgLoader.Reload(); err != nil {
		writeError(w, http.StatusInternalServerError, "config_reload_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reloaded": true})
}

// redactValue reads the live config snapshot's redact value, falling back to
// the "on" default when no loader is wired.
func (s *Server) redactValue() string {
	if s.cfgLoader != nil {
		return config.CanonicalRedact(s.cfgLoader.Current().RedactPolicy)
	}
	return "on"
}

// rootsMap returns the live per-provider resolved roots; empty when no loader
// is wired.
func (s *Server) rootsMap() map[string][]string {
	if s.cfgLoader != nil {
		return s.cfgLoader.Current().Roots
	}
	return map[string][]string{}
}

func retentionPolicyView(p retention.Policy) map[string]any {
	return map[string]any{
		"profile":          string(p.Profile),
		"raw_age":          p.RawAge.String(),
		"redact_raw_after": p.RedactRawAfter.String(),
	}
}

func (s *Server) handleRetentionProfiles(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, p := range []retention.Profile{retention.ProfilePrivacy, retention.ProfileBalanced, retention.ProfileArchive} {
		policy, err := retention.PolicyFor(p)
		if err != nil {
			continue
		}
		out = append(out, retentionPolicyView(policy))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRetentionStatus(w http.ResponseWriter, r *http.Request) {
	profile := r.URL.Query().Get("profile")
	if profile == "" {
		profile = "balanced"
	}
	policy, err := retention.PolicyFor(retention.Profile(profile))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_profile", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, retentionPolicyView(policy))
}

func (s *Server) handleRetentionPrune(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
		DryRun  *bool  `json:"dry_run"`
	}
	if !decodeJSONBody(w, r, maxControlRequestBytes, &req) {
		return
	}
	q := r.URL.Query()
	profile := firstNonEmpty(req.Profile, q.Get("profile"))
	if profile == "" {
		profile = "balanced"
	}
	policy, err := retention.PolicyFor(retention.Profile(profile))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_profile", err.Error())
		return
	}
	dryRun := pruneDryRun(req.DryRun, q)
	// Wrap the event log so its Prune coordinates with in-flight reconnect
	// replays via replayMu (see replayGuardedStore); a bare s.eventStore would
	// delete ranges out from under a replay.
	events := replayGuardedStore{Store: s.eventStore, mu: &s.replayMu}
	report, err := retention.Apply(r.Context(), s.store, events, policy, time.Now().UTC(), dryRun)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "retention_apply_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleDataPruneRaw(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OlderThan string `json:"older_than"`
		DryRun    *bool  `json:"dry_run"`
	}
	if !decodeJSONBody(w, r, maxControlRequestBytes, &req) {
		return
	}
	q := r.URL.Query()
	olderThan := firstNonEmpty(req.OlderThan, q.Get("older_than"))
	if olderThan == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "older_than is required (e.g. 720h)")
		return
	}
	// Shared validation/cutoff with the CLI runAgePrune path — a non-positive age is
	// rejected so neither surface can wipe the whole raw_events table.
	cutoff, err := retention.RawPruneCutoff(olderThan, time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_duration", err.Error())
		return
	}
	dryRun := pruneDryRun(req.DryRun, q)
	count, err := s.store.PruneRawEvents(r.Context(), cutoff, dryRun)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "prune_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"raw_events": count, "dry_run": dryRun})
}
