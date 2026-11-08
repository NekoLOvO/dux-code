pragma Singleton

import QtQuick
import Quickshell
import qs.Common
import qs.Services

// System notifications: the three "you are needed" moments, sent to Dux Shell's notification center.
//
// One rule: when the window is focused and the event belongs to the open session, you already
// saw it, so skip the popup. A closed window (shell.qml unloads ChatWindow) reports
// windowActive false, which is exactly when a notification matters.
//
// Sends via notify-send rather than Quickshell's NotificationServer: that one receives other
// apps' notifications, while Dux Shell is already the notification daemon.
Singleton {
    id: root

    // Bound by shell.qml: the main window exists and holds focus.
    property bool windowActive: false

    readonly property string appName: "Dux Code"
    readonly property string iconName: "com.nekolovo.dcode"

    // true while you are looking, so no popup.
    function _quiet(sessionId) {
        if (!root.windowActive)
            return false;
        if (sessionId === "")
            return true;
        return sessionId === Sessions.activeId;
    }

    function _titleFor(sessionId) {
        const s = sessionId !== "" ? Sessions.find(sessionId) : null;
        const t = s && s.title ? String(s.title) : "";
        return t !== "" ? t : I18n.tr("New session", "system notification text");
    }

    // A notification body cannot hold a reply; collapse to one line and truncate.
    function _snippet(text, max) {
        const flat = String(text || "").replace(/\s+/g, " ").trim();
        if (flat.length <= max)
            return flat;
        return flat.slice(0, max - 1) + "…";
    }

    function send(title, body, urgency, sound) {
        Quickshell.execDetached(["notify-send", "-a", root.appName, "-i", root.iconName, "-u", urgency || "normal", "-t", "6000", String(title || ""), String(body || "")]);
        if (sound !== "" && SettingsData.notificationSounds)
            Quickshell.execDetached(["canberra-gtk-play", "-i", sound]);
    }

    Connections {
        target: Backend

        function onAgentEvent(data) {
            const kind = data.kind || "";
            const sid = data.sessionId || "";

            if (kind === "run.finished") {
                // A manual stop is not worth reporting.
                if ((data.status || "") === "cancelled")
                    return;
                if (!SettingsData.notifyAgentDone || root._quiet(sid))
                    return;
                const body = data.message && typeof data.message.text === "string" ? root._snippet(data.message.text, 160) : "";
                root.send(root._titleFor(sid), body !== "" ? body : I18n.tr("Reply finished.", "system notification text"), "normal", "complete");
                return;
            }

            if (kind === "run.failed") {
                if (!SettingsData.notifyAgentError || root._quiet(sid))
                    return;
                const err = root._snippet(data.error || "", 160);
                root.send(I18n.tr("Reply failed · %1", "system notification text").arg(root._titleFor(sid)), err !== "" ? err : I18n.tr("The run stopped with an error.", "system notification text"), "critical", "dialog-error");
            }
        }

        // Scheduled tasks: completion usually goes unwatched, so notify more eagerly here.
        // Failures use the error toggle, success the done toggle.
        function onTaskEvent(data) {
            if ((data.kind || "") !== "task.finished")
                return;
            // A cancellation is not a result (same as chat). The task runs again next time; a
            // red failure banner would only alarm.
            if ((data.status || "") === "cancelled")
                return;
            const ok = (data.status || "") === "ok";
            if (ok ? !SettingsData.notifyAgentDone : !SettingsData.notifyAgentError)
                return;
            if (root._quiet(data.sessionId || ""))
                return;
            const name = data.name ? String(data.name) : I18n.tr("Scheduled task", "system notification text");
            if (ok) {
                root.send(name, I18n.tr("Scheduled run finished.", "system notification text"), "normal", "complete");
                return;
            }
            const detail = root._snippet(data.detail || "", 140);
            root.send(I18n.tr("%1 · failed", "system notification text").arg(name), detail !== "" ? detail : I18n.tr("The run stopped with an error.", "system notification text"), "critical", "dialog-error");
        }

        function onPermissionPending() {
            // Permission requests carry no sessionId, so check the window only: in the
            // foreground PermissionDialog already appears.
            if (!SettingsData.notifyAgentPermission || root.windowActive)
                return;
            const reqs = Backend.permissionRequests;
            if (!reqs || reqs.length === 0)
                return;
            const r = reqs[0];
            const rest = reqs.length > 1 ? I18n.tr("%1 more waiting", "system notification text").arg(reqs.length - 1) : "";
            const detail = root._snippet(r.detail || "", 140);
            root.send(r.title !== "" ? r.title : I18n.tr("Permission needed", "system notification text"), detail !== "" ? detail : rest, "normal", "dialog-warning");
        }
    }
}
