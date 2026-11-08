pragma Singleton
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import qs.Common
import qs.Services

// Sessions — data layer for the conversation list.
//
// Everything comes from the daemon (session.list). This only splits the flat list into
// sidebar groups (pinned / today / this week / this month / earlier) and tracks the
// active and running session. Groups are never persisted: time buckets change daily.
Singleton {
    id: root

    readonly property var log: Log.scoped("Sessions")

    // Raw list from the daemon, already sorted (pinned first, then most recently updated).
    property var sessions: []
    property string activeId: ""
    // Whether the daemon has been asked at least once: separates "not asked yet" from "asked, none
    // found". Without it the sidebar says "No sessions yet" while it is really still loading, which
    // is exactly how a list that never arrived looks like an empty one.
    property bool loaded: false

    // When each running session's turn began, epoch ms (0 or absent = idle).
    //
    // Per session, and taken from the daemon rather than from "when this window first noticed". The one
    // shared counter this replaces froze the moment the user looked at another conversation (the ticker
    // stopped with the active session) and restarted at 0 on the way back, so a reply could be two
    // minutes old and the sidebar would say 1s.
    property var runStarts: ({})

    // Re-evaluation pulse for every elapsed label. The labels call elapsedFor(), which reads this, so a
    // tick re-evaluates all of them at once — including sessions that are not the active one.
    property int clockTick: 0

    readonly property Timer busyTicker: Timer {
        interval: 500
        repeat: true
        // Only while something is running: an idle sidebar should not wake up twice a second.
        running: root.anyBusy
        onTriggered: root.clockTick++
    }

    // How long the session's turn has been going. 0 when it is not running.
    function elapsedFor(id) {
        root.clockTick; // dependency: the binding re-evaluates on every tick
        const start = root.runStarts[id];
        if (!start || start <= 0)
            return 0;
        return Math.max(0, Date.now() - start);
    }

    readonly property var activeSession: {
        for (let i = 0; i < sessions.length; i++) {
            if (sessions[i].id === activeId)
                return sessions[i];
        }
        return null;
    }

    readonly property string activeTitle: activeSession ? activeSession.title : I18n.tr("New session", "sidebar session title when nothing is selected")

    readonly property bool anyBusy: {
        for (let i = 0; i < sessions.length; i++) {
            if (sessions[i].busy === true)
                return true;
        }
        return false;
    }

    function formatElapsed(ms) {
        const total = Math.max(0, Math.round(ms / 1000));
        if (total < 60)
            return total + "s";
        const m = Math.floor(total / 60);
        const s = total % 60;
        return m + "m " + (s < 10 ? "0" + s : s) + "s";
    }

    function find(id) {
        for (let i = 0; i < sessions.length; i++) {
            if (sessions[i].id === id)
                return sessions[i];
        }
        return null;
    }

    // --- grouping ---
    function bucketOf(millis) {
        const now = new Date();
        const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
        if (millis >= startOfToday)
            return "today";
        if (millis >= startOfToday - 6 * 86400000)
            return "week";
        const d = new Date(millis);
        if (d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth())
            return "month";
        return "older";
    }

    readonly property var allGroups: {
        const buckets = {
            "pinned": [],
            "today": [],
            "week": [],
            "month": [],
            "older": []
        };
        for (let i = 0; i < sessions.length; i++) {
            const s = sessions[i];
            // Empty sessions stay out of the sidebar but remain in sessions, so a reload keeps
            // activeId and the title bar can still resolve it.
            if ((s.messageCount || 0) === 0)
                continue;
            if (s.pinned === true)
                buckets.pinned.push(s);
            else
                buckets[root.bucketOf(s.updatedAt || 0)].push(s);
        }

        const labels = {
            "pinned": I18n.tr("Pinned", "sidebar session group"),
            "today": I18n.tr("Today", "sidebar session group"),
            "week": I18n.tr("This week", "sidebar session group"),
            "month": I18n.tr("This month", "sidebar session group"),
            "older": I18n.tr("Earlier", "sidebar session group")
        };

        const order = ["pinned", "today", "week", "month", "older"];
        const out = [];
        for (let i = 0; i < order.length; i++) {
            const key = order[i];
            if (buckets[key].length === 0)
                continue;
            out.push({
                "id": key,
                "label": labels[key],
                "sessions": buckets[key]
            });
        }
        return out;
    }

    // Only return groups that still have sessions, so empty ones disappear.
    function visibleGroups() {
        return allGroups;
    }

    function flat() {
        return sessions;
    }

    // --- sync with the daemon ---
    function reload(done) {
        if (!Backend.connected) {
            sessions = [];
            loaded = false;
            if (done)
                done();
            return;
        }
        Backend.sendRequest("session.list", {}, (res, err) => {
            if (err || !res) {
                root.log.warn("session.list failed: " + err);
                if (done)
                    done();
                return;
            }
            root.sessions = res.sessions || [];
            root.loaded = true;

            // The list carries each running session's start time, which is the only source for a turn
            // this window never saw begin: a fresh window, a reconnect, or a turn the CLI started.
            //
            // Merged, never rebuilt: a `run.started` event may already have reported a run this list
            // was fetched before, and taking the start back would restart the counter at 0s. The earlier
            // of the two wins, and an entry for a session that is no longer listed is forgotten.
            let starts = {};
            const known = root.runStarts || {};
            for (let i = 0; i < root.sessions.length; i++) {
                const s = root.sessions[i];
                if (!s || !s.id)
                    continue;
                const prev = known[s.id] || 0;
                if (s.busy === true) {
                    if (s.startedAt > 0 && (prev === 0 || s.startedAt < prev))
                        starts[s.id] = s.startedAt;
                    else if (prev > 0)
                        starts[s.id] = prev;
                }
            }
            root.runStarts = starts;

            // Pick the first when the active one is gone.
            let found = false;
            for (let i = 0; i < root.sessions.length; i++) {
                if (root.sessions[i].id === root.activeId) {
                    found = true;
                    break;
                }
            }
            if (root.sessions.length === 0) {
                root.create();
            } else if (!found) {
                root.activeId = root.sessions[0].id;
            }
            // done fires after activeId settles: the caller may want to select a session that
            // just appeared (a finished task run does this).
            if (done)
                done();
        });
    }

    function select(id) {
        if (id === root.activeId)
            return;
        root.activeId = id;
    }

    // New session; switch to it once the daemon returns an id.
    function create() {
        if (!Backend.connected)
            return;
        // The working directory follows the session: a new one inherits the current setting.
        const wd = SettingsData.workingDirectory !== "" ? SettingsData.workingDirectory : Quickshell.env("HOME");
        Backend.sendRequest("session.new", { "workingDirectory": wd }, (res, err) => {
            if (err || !res || !res.session) {
                root.log.warn("session.new failed: " + err);
                return;
            }
            root.sessions = [res.session].concat(root.sessions);
            root.activeId = res.session.id;
        });
    }

    // Titles are generated by the daemon after the first message; this is the manual rename.
    function rename(id, title) {
        const clean = String(title || "").trim();
        if (clean === "")
            return;
        Backend.sendRequest("session.rename", {
            "id": id,
            "title": clean
        }, (res, err) => {
            if (err) {
                root.log.warn("session.rename failed: " + err);
                return;
            }
            root._patch(id, {
                "title": clean
            });
        });
    }

    function togglePin(id) {
        const s = root.find(id);
        if (!s)
            return;
        const next = s.pinned !== true;
        Backend.sendRequest("session.pin", {
            "id": id,
            "pinned": next
        }, (res, err) => {
            if (err) {
                root.log.warn("session.pin failed: " + err);
                return;
            }
            root._patch(id, {
                "pinned": next
            });
        });
    }

    function remove(id) {
        Backend.sendRequest("session.delete", {
            "id": id
        }, (res, err) => {
            if (err) {
                root.log.warn("session.delete failed: " + err);
                return;
            }
            const kept = root.sessions.filter(s => s.id !== id);
            root.sessions = kept;
            if (root.activeId === id) {
                // The open session was deleted: start a new empty one instead of jumping to the
                // previous one, which reads as "delete turned into switch".
                root.activeId = "";
                root.create();
            } else if (kept.length === 0) {
                root.create();
            }
        });
    }

    // Patch one field locally instead of refetching everything.
    function _patch(id, fields) {
        const next = sessions.map(s => s.id === id ? Object.assign({}, s, fields) : s);
        sessions = next;
    }

    // Note when a session's turn began, without ever shortening a known start: a session.list reply and
    // a run.started event describe the same run, and the earlier fact wins.
    function noteStart(id, startedAt) {
        if (!id)
            return;
        const prev = root.runStarts[id] || 0;
        if (startedAt > 0) {
            if (prev === startedAt)
                return;
            const next = Object.assign({}, root.runStarts);
            next[id] = startedAt;
            root.runStarts = next;
            return;
        }
        if (prev === 0)
            return;
        const next = Object.assign({}, root.runStarts);
        delete next[id];
        root.runStarts = next;
    }

    // Losing the connection means the events that would have ended a turn are gone, so a start time
    // kept across the gap can outlive its run. The reload that follows reconnection fills them in.
    readonly property Connections _linkState: Connections {
        target: Backend
        function onConnectionStateChanged() {
            if (!Backend.connected)
                root.runStarts = ({});
        }
    }

    // Ask once on creation, because this singleton is created on first use and the first use may be
    // long after the handshake.
    //
    // A hidden start (login autostart) keeps the window in an inactive Loader, so nothing reads
    // Sessions until the user finally opens it — by which time the one sessionsStale the handshake
    // emitted has already gone to nobody. The list then stayed empty until the next session.* call,
    // which looked like "the sessions are gone" or "only a restart finds them". Skills does the same
    // for the same reason; anything loading its state from one signal needs this second entry point.
    Component.onCompleted: {
        if (Backend.connected)
            reload();
    }

    onActiveIdChanged: Chat.loadSession(activeId)

    readonly property Connections _backendLink: Connections {
        target: Backend

        function onSessionsStale() {
            root.reload();
        }

        // Agent events change both busy state and title.
        function onAgentEvent(data) {
            if (!data)
                return;
            const kind = data.kind || "";
            const sid = data.sessionId || "";
            if (sid === "")
                return;
            if (kind === "run.started") {
                // Show it in the sidebar as soon as the first message is sent, not at run.finished.
                const cur = root.find(sid);
                root._patch(sid, {
                    "busy": true,
                    "messageCount": Math.max(1, cur ? (cur.messageCount || 0) : 0)
                });
                // startedAt is the daemon's, so the counter shows the turn's real age from the first
                // frame, even in a window that loaded after the turn began.
                root.noteStart(sid, data.startedAt || 0);
            } else if (kind === "run.finished" || kind === "run.failed") {
                root._patch(sid, {
                    "busy": false
                });
                root.noteStart(sid, 0);
                // The daemon may have just auto-named it.
                root.reload();
            } else if (kind === "session.renamed") {
                // Auto-naming is async and lands after run.finished.
                const t = data.title || "";
                if (t !== "")
                    root._patch(sid, {
                        "title": t
                    });
            }
        }
    }
}
