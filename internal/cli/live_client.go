package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"toktop.unceas.dev/internal/liveevent"
	"toktop.unceas.dev/internal/query"
	"toktop.unceas.dev/internal/retention"
	"toktop.unceas.dev/internal/store/sqlite"
)

// pruneRetentionViaServer asks the daemon to run a retention prune. The daemon
// is the sole owner of the event log, so the event-log portion of a prune must
// run there. Returns errStreamServerUnreachable when no daemon is up so the
// caller can fall back to a local, sqlite-only prune.
func pruneRetentionViaServer(ctx context.Context, addr, token, profile string, dryRun bool) (retention.Report, error) {
	q := url.Values{}
	q.Set("profile", profile)
	q.Set("dry_run", strconv.FormatBool(dryRun))
	resp, err := apiRequest(ctx, http.MethodPost, addr, "/v1/data/retention:prune?"+q.Encode(), token, nil)
	if err != nil {
		if ctx.Err() != nil {
			return retention.Report{}, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return retention.Report{}, err
		}
		return retention.Report{}, fmt.Errorf("%w: %v", errStreamServerUnreachable, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return retention.Report{}, fmt.Errorf("retention auth failed (%s): %s; pass --token or use --no-auth", resp.Status, strings.TrimSpace(string(body)))
		}
		return retention.Report{}, fmt.Errorf("retention request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var report retention.Report
	if err := json.Unmarshal(body, &report); err != nil {
		return retention.Report{}, fmt.Errorf("decode retention response: %w", err)
	}
	return report, nil
}

// liveStatusFromServer fetches the daemon's /v1/status snapshot. That endpoint
// overlays the in-memory broker state on top of the stored sessions, so it is
// the same fresh, consistent view /v1/stream and downstream SSE consumers see.
// Returns errStreamServerUnreachable when the daemon can't be contacted so the
// caller can fall back to a direct (overlay-less) store read.
func liveStatusFromServer(ctx context.Context, addr, token string, q url.Values) ([]sqlite.LiveSessionItem, error) {
	path := "/v1/status"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	resp, err := apiRequest(ctx, http.MethodGet, addr, path, token, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", errStreamServerUnreachable, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("status auth failed (%s): %s; pass --token or use --no-auth", resp.Status, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("status request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var page query.Page[sqlite.LiveSessionItem]
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}
	return page.Items, nil
}

// errStreamServerUnreachable signals the live server could not be contacted at
// the transport level (no daemon / connection refused), so the caller can print
// actionable guidance instead of a raw dial error. A reached server that
// answers non-200 (e.g. auth failure) returns a different, descriptive error.
var errStreamServerUnreachable = errors.New("live server unreachable")

// streamFromServer subscribes to the daemon's /v1/stream SSE endpoint and feeds
// each live event to emit. The daemon is the single acquirer of the event log;
// the CLI is just another fan-out consumer that formats output. The server
// applies the same watch/status_only filtering, so this only decodes + emits.
func streamFromServer(ctx context.Context, addr, token string, targets []liveevent.Target, statusOnly bool, emit func(liveevent.Event) error) error {
	client, base := httpClientFor(addr, 0)
	u, err := url.Parse(strings.TrimSuffix(base, "/") + "/v1/stream")
	if err != nil {
		return fmt.Errorf("%w: %v", errStreamServerUnreachable, err)
	}
	q := u.Query()
	for _, t := range targets {
		q.Add("watch", t.Provider+":"+t.Session)
	}
	if statusOnly {
		q.Set("status_only", "true")
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("%w: %v", errStreamServerUnreachable, err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// No client timeout: this is a long-lived stream bounded only by ctx.
	resp, err := client.Do(req)
	if err != nil {
		// Connection-level failure (no daemon / refused). Let the caller decide
		// how to surface "start a daemon" guidance.
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("%w: %v", errStreamServerUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("live stream auth failed (%s): %s; pass --token or use --no-auth", resp.Status, msg)
		}
		return fmt.Errorf("live stream request failed: %s: %s", resp.Status, msg)
	}
	return consumeSSE(ctx, resp.Body, emit)
}

// consumeSSE parses a text/event-stream and emits live events. Control frames
// (hello/ping/resync_required/replay.error) and any frame whose data is not a
// live event are skipped; the server already filtered by watch/status_only.
func consumeSSE(ctx context.Context, body io.Reader, emit func(liveevent.Event) error) error {
	reader := bufio.NewReader(body)
	var eventType string
	var dataLines []string
	dispatch := func() error {
		etype, lines := eventType, dataLines
		eventType, dataLines = "", nil
		switch etype {
		case "", "hello", "ping", "resync_required", "replay.error":
			return nil
		}
		if len(lines) == 0 {
			return nil
		}
		var ev liveevent.Event
		if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &ev); err != nil || ev.Type == "" {
			return nil
		}
		return emit(ev)
	}
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			switch trimmed := strings.TrimRight(line, "\r\n"); {
			case trimmed == "":
				if derr := dispatch(); derr != nil {
					return derr
				}
			case strings.HasPrefix(trimmed, ":"):
				// comment / keep-alive; ignore
			case strings.HasPrefix(trimmed, "event:"):
				eventType = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			case strings.HasPrefix(trimmed, "data:"):
				// SSE strips one optional leading space after the colon.
				dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
			}
		}
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("live stream interrupted: %w", err)
		}
	}
}
