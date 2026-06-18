// toktop observer plugin for opencode.
//
// Installed by `toktop hooks install --sources=opencode`; opencode auto-loads any
// file under its plugins/ dir at startup. This pushes live session status to the
// toktop daemon's /v1/hooks:intake so dashboards/status widgets react in real time.
//
// It is BEST-EFFORT: it never throws and never blocks the host turn — a throwing
// or awaited plugin hook can stall opencode (opencode#16879), so the POST is
// fire-and-forget and every error is swallowed.
//
// ENDPOINT / UNIX / TOKEN are substituted by the installer at write time. The JSON
// keys are exactly what toktop's intake accepts (no toktop change needed).
const ENDPOINT = "__TOKTOP_ENDPOINT__";
const UNIX = "__TOKTOP_UNIX__";
const TOKEN = "__TOKTOP_TOKEN__";

// The opencode bus events that carry a meaningful live-status transition. Keep in
// lockstep with the Go PluginEventStatus map (both owned by the opencode provider).
const WATCH = new Set([
  "session.created",
  "session.updated",
  "session.idle",
  "session.error",
  "permission.updated",
  "message.updated",
]);

export const ToktopObserver = async ({ directory, project }) => ({
  event: async ({ event }) => {
    try {
      if (!event || !WATCH.has(event.type)) return;
      const p = event.properties || {};
      // sessionID is on idle/error/permission; session.created/updated carry the
      // full Session as .info; message.updated carries the Message as .info.
      const sid = p.sessionID || (p.info && (p.info.id || p.info.sessionID)) || "";
      const reason = (p.error && (p.error.message || p.error.name)) || "";
      const body = JSON.stringify({
        type: event.type,
        provider: "opencode",
        session_id: sid,
        reason: reason,
        project_path: directory || (project && project.worktree) || "",
        timestamp: new Date().toISOString(),
      });
      const headers = { "Content-Type": "application/json" };
      if (TOKEN) headers["Authorization"] = "Bearer " + TOKEN;
      const opts = { method: "POST", headers, body };
      // Bun's fetch accepts a unix-socket option; on a runtime without it the POST
      // simply fails and is swallowed (toktop falls back to transcript watching).
      if (UNIX) opts.unix = UNIX;
      Promise.resolve(fetch(ENDPOINT, opts)).catch(() => {});
    } catch (_) {
      /* never throw out of a plugin hook */
    }
  },
});
