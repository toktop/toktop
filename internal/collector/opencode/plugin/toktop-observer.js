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

// FORWARD: opencode bus events that carry a live-status TRANSITION
// (busy/idle/failed/awaiting), POSTed to toktop and kept in lockstep with the Go
// PluginEventStatus map (both owned by the opencode provider). The authoritative
// busy/idle signal is `session.status` (its properties.status.type). Content/metadata
// updates (session.updated, message.updated) are NOT forwarded: opencode emits them
// right AFTER session.idle as end-of-turn bookkeeping, and forwarding them flipped a
// finished session back to active. Permission prompts are permission.asked /
// permission.replied (opencode emits no "permission.updated").
const FORWARD = new Set([
  "session.status",
  "session.idle",
  "session.error",
  "permission.asked",
  "permission.replied",
]);

// TRACK: events watched ONLY to learn which sessions are subagents, never forwarded.
// toktop's live status is top-level only, so a subagent's status events must be
// dropped at the producer (else they inject a phantom row into `toktop status`). But
// the lean status events (session.status/idle/error) carry only a sessionID — never
// the parentID that marks a subagent — so we learn it from session.created/updated,
// whose properties.info is the full Session (info.parentID set ⇒ subagent).
const TRACK = new Set(["session.created", "session.updated"]);

export const ToktopObserver = async ({ directory, project }) => {
  // Session ids known to be subagents (child sessions, Session.parentID set). Drop
  // their status events so live status stays top-level only. Best-effort: a subagent
  // whose create/update is never observed (e.g. it predates plugin load) isn't known
  // and could leak once — opencode loads plugins at startup, so in practice every
  // subagent is created after we begin observing.
  const subagents = new Set();
  return {
    event: async ({ event }) => {
      try {
        if (!event) return;
        const p = event.properties || {};
        if (TRACK.has(event.type)) {
          const info = p.info;
          if (info && info.id && info.parentID) subagents.add(info.id);
          return; // tracking only — not a status transition to forward
        }
        if (!FORWARD.has(event.type)) return;
        // sessionID is present on status/idle/error/permission events.
        const sid = p.sessionID || (p.info && (p.info.id || p.info.sessionID)) || "";
        if (sid && subagents.has(sid)) return; // subagent: top-level live status only
        // session.status carries the busy/idle/retry transition in
        // properties.status.type; synthesize a distinct event name
        // (session.status.<type>) so toktop's name→status map can tell them apart.
        // Other forwarded events map by their bare name. Skip a status with no type.
        let type = event.type;
        if (type === "session.status") {
          const st = p.status && p.status.type;
          if (!st) return;
          type = "session.status." + st;
        }
        const reason = (p.error && (p.error.message || p.error.name)) || "";
        const body = JSON.stringify({
          type: type,
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
  };
};
