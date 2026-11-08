pragma Singleton
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import qs.Services

// Skills — single source of truth for the skills the agent can use.
//
// Previously only the settings page called skills.list, so finding out what the agent can do
// required knowing to look there. The composer chip and the settings page now share this
// list, so a newly installed skill appears in both.
//
// Scanning runs in the daemon (~/.agents/skills/<name>/SKILL.md); nothing here touches the
// filesystem or caches to disk.
Singleton {
    id: root

    readonly property var log: Log.scoped("Skills")

    property var list: []
    // Folders that look like skills but are not (only test.md, or no description). Surfaced
    // so a silent no-op is visible.
    property var skipped: []
    property string dir: ""
    // Whether the daemon has been asked at least once: separates "not asked yet" from
    // "asked, none found", which otherwise flashes an empty state.
    property bool loaded: false

    readonly property int count: list.length

    function refresh() {
        if (!Backend.connected)
            return;
        Backend.sendRequest("skills.list", null, (res, err) => {
            if (err || !res) {
                root.log.warn("skills.list failed: " + err);
                return;
            }
            root.list = res.skills || [];
            root.skipped = res.skipped || [];
            root.dir = res.dir || "";
            root.loaded = true;
        });
    }

    Component.onCompleted: refresh()

    readonly property Connections _link: Connections {
        target: Backend

        function onConnectionStateChanged() {
            if (Backend.connected)
                root.refresh();
        }
    }
}
