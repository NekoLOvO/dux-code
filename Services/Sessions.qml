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

    // Sidebar running indicator (single agent).
    property double busySince: 0
    property int busyElapsedMs: 0

    readonly property Timer busyTicker: Timer {
        interval: 500
        repeat: true
        running: root.busySince > 0
        onTriggered: root.busyElapsedMs = Date.now() - root.busySince
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

    // Sidebar busy indicator: follows the active session.
    function syncBusy() {
        const s = root.find(root.activeId);
        const busy = s ? s.busy === true : false;
        if (busy) {
            if (root.busySince === 0) {
                root.busySince = Date.now();
                root.busyElapsedMs = 0;
            }
        } else {
            root.busySince = 0;
        }
    }

    onActiveIdChanged: {
        Chat.loadSession(activeId);
        syncBusy();
    }

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
                root.syncBusy();
            } else if (kind === "run.finished" || kind === "run.failed") {
                root._patch(sid, {
                    "busy": false
                });
                root.syncBusy();
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
