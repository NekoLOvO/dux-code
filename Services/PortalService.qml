pragma Singleton
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Io

// System light/dark preference.
//
// Reads org.freedesktop.appearance color-scheme from the XDG Desktop Portal:
// 0 = no preference (most desktops also report light as 0), 1 = dark, 2 = light.
//
// Asks the portal directly instead of going through the daemon: this is an appearance
// preference, not an LLM backend concern, and must work with the daemon down.
Singleton {
    id: root

    property int systemColorScheme: 0
    property bool available: false

    // Stay dark (this app's default) when no portal is available.
    readonly property bool systemPrefersLight: available && systemColorScheme !== 1

    function _applyLine(line) {
        // gdbus prints e.g. (<<uint32 1>>,); the wrapper depth varies, so find the first uint32.
        const match = /uint32\s+(\d+)/.exec(line || "");
        if (!match)
            return;
        available = true;
        systemColorScheme = parseInt(match[1], 10);
    }

    function refresh() {
        if (!reader.running)
            reader.running = true;
    }

    Process {
        id: reader

        command: ["gdbus", "call", "--session", "--dest", "org.freedesktop.portal.Desktop", "--object-path", "/org/freedesktop/portal/desktop", "--method", "org.freedesktop.portal.Settings.Read", "org.freedesktop.appearance", "color-scheme"]
        running: false

        stdout: SplitParser {
            onRead: line => root._applyLine(line)
        }

        onExited: code => {
            if (code !== 0) {
                // No portal (common on plain wlroots) means no preference.
                root.available = false;
                root.systemColorScheme = 0;
            }
            // Let the next refresh() run again.
            reader.running = false;
        }
    }

    // The portal broadcasts SettingChanged when the desktop theme flips. This listener is
    // long-lived; it exits immediately and does not retry when no portal exists.
    Process {
        id: monitor

        command: ["gdbus", "monitor", "--session", "--dest", "org.freedesktop.portal.Desktop", "--object-path", "/org/freedesktop/portal/desktop"]
        running: true

        stdout: SplitParser {
            onRead: line => {
                if (line.indexOf("SettingChanged") >= 0 && line.indexOf("color-scheme") >= 0)
                    root.refresh();
            }
        }
    }

    Component.onCompleted: refresh()
}
