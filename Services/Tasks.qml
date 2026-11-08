pragma Singleton
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import qs.Common
import qs.Services

// Tasks — data layer for scheduled tasks.
//
// Scheduling and the next run time are computed by the daemon (including catch-up runs);
// this only lists and edits. A UI-side schedule would drift.
Singleton {
    id: root

    readonly property var log: Log.scoped("Tasks")

    property var tasks: []

    function reload(done) {
        if (!Backend.connected) {
            tasks = [];
            if (done)
                done();
            return;
        }
        Backend.sendRequest("task.list", {}, (res, err) => {
            if (err || !res) {
                root.log.warn("task.list failed: " + err);
            } else {
                root.tasks = res.tasks || [];
            }
            if (done)
                done();
        });
    }

    function save(params, cb) {
        Backend.sendRequest("task.save", params, (res, err) => {
            if (!err)
                root.reload();
            if (cb)
                cb(res, err);
        });
    }

    function remove(id, cb) {
        Backend.sendRequest("task.delete", {
            "id": id
        }, (res, err) => {
            if (!err)
                root.reload();
            if (cb)
                cb(res, err);
        });
    }

    // Run now: the task runs in a new session and we switch to it, since that is what the
    // user wants to see.
    function run(id, cb) {
        Backend.sendRequest("task.run", {
            "id": id
        }, (res, err) => {
            if (err) {
                if (cb)
                    cb(res, err);
                return;
            }
            root.reload();
            const sid = res && res.sessionId ? res.sessionId : "";
            if (sid !== "") {
                // Wait for the session list, or select targets an id that does not exist yet.
                Sessions.reload(function () {
                    Sessions.select(sid);
                });
            }
            if (cb)
                cb(res, null);
        });
    }

    // Human-readable schedule for the UI.
    function scheduleLabel(task) {
        const kind = task.kind || "manual";
        if (kind === "daily")
            return I18n.tr("Every day at %1", "task schedule: daily at a time").arg(task.at || "");
        if (kind === "weekly")
            return I18n.tr("Every %1 at %2", "task schedule: weekly on a weekday").arg(root.weekdayName(task.weekday)).arg(task.at || "");
        if (kind === "every")
            return I18n.tr("Every %1 minutes", "task schedule: fixed interval in minutes").arg(task.minutes || 0);
        return I18n.tr("Manual only", "task schedule: never runs on its own");
    }

    function weekdayName(index) {
        const names = [
            I18n.tr("Sunday", "weekday name"),
            I18n.tr("Monday", "weekday name"),
            I18n.tr("Tuesday", "weekday name"),
            I18n.tr("Wednesday", "weekday name"),
            I18n.tr("Thursday", "weekday name"),
            I18n.tr("Friday", "weekday name"),
            I18n.tr("Saturday", "weekday name")
        ];
        return names[Math.max(0, Math.min(6, index | 0))];
    }

    function nextRunLabel(task) {
        if (!task.nextRunAt)
            return "";
        const d = new Date(task.nextRunAt);
        return Qt.formatDateTime(d, "MM-dd HH:mm");
    }
}
