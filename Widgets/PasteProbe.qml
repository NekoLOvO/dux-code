import QtQuick
import Quickshell
import Quickshell.Io
import qs.Common

// Decides whether a paste is a file or plain text.
//
// Copying a file in a file manager puts both text/uri-list (the real "this is a
// file") and text/plain (its path) on the clipboard. Quickshell only exposes
// text/plain, so this asks wl-paste for the type list and turns files into
// attachments instead of a bare path.
//
// Without wl-paste, or outside Wayland, it falls back to a plain-text paste.
Item {
    id: root

    // paths are absolute.
    signal filesFound(var paths)
    signal textFound(string text)

    // Same formats as the daemon's sniffImage; others are rejected downstream anyway.
    readonly property var imageTypes: ["image/png", "image/jpeg", "image/gif", "image/webp"]
    readonly property string tempDir: Quickshell.env("XDG_RUNTIME_DIR") || "/tmp"

    // One paste, one answer: the flag makes each branch report exactly once and
    // prevents a duplicate insert when both processes return.
    property bool _pending: false
    property string _imagePath: ""

    function probe() {
        if (root._pending)
            return;
        root._pending = true;
        watchdog.restart();
        typesProc.running = true;
    }

    function settle() {
        root._pending = false;
        watchdog.stop();
    }

    function fallbackToText() {
        if (!root._pending)
            return;
        root.settle();
        root.textFound(String(Quickshell.clipboardText || ""));
    }

    function handleTypes(raw) {
        if (!root._pending)
            return;
        const types = String(raw || "").split("\n").map(s => s.trim()).filter(s => s !== "");
        if (types.length === 0) {
            // Empty clipboard, or wl-paste never started.
            root.fallbackToText();
            return;
        }
        if (types.indexOf("text/uri-list") >= 0) {
            watchdog.restart();
            uriProc.running = true;
            return;
        }
        // The clipboard holds image bytes rather than a path (screenshot tool,
        // "copy image"), so write a temp file to attach.
        for (let i = 0; i < root.imageTypes.length; i++) {
            if (types.indexOf(root.imageTypes[i]) >= 0) {
                root.saveImage(root.imageTypes[i]);
                return;
            }
        }
        root.fallbackToText();
    }

    function handleUriList(raw) {
        if (!root._pending)
            return;
        const paths = [];
        const lines = String(raw || "").split(/\r?\n/);
        for (let i = 0; i < lines.length; i++) {
            const line = lines[i].trim();
            // uri-list allows comment lines starting with #.
            if (line === "" || line.charAt(0) === "#")
                continue;
            const p = root.uriToPath(line);
            if (p !== "")
                paths.push(p);
        }
        root.settle();
        if (paths.length === 0) {
            // text/uri-list without a file: URI (e.g. a link); paste as text.
            root.textFound(String(Quickshell.clipboardText || ""));
            return;
        }
        root.filesFound(paths);
    }

    // file:///a/b -> /a/b. URIs with a host are not handled: they do not occur on
    // Wayland, and resolving one would only yield a nonexistent path.
    function uriToPath(uri) {
        const s = String(uri || "");
        if (s.indexOf("file://") !== 0)
            return "";
        const rest = s.slice(7);
        if (rest.charAt(0) !== "/")
            return "";
        try {
            return decodeURIComponent(rest);
        } catch (e) {
            return rest;
        }
    }

    function saveImage(mime) {
        const ext = mime === "image/jpeg" ? "jpg" : mime.slice(6);
        root._imagePath = root.tempDir + "/dcode-paste-" + Date.now() + "." + ext;
        watchdog.restart();
        imageProc.command = ["sh", "-c", "wl-paste --no-newline --type '" + mime + "' > '" + root._imagePath + "'"];
        imageProc.running = true;
    }

    // Recover from a stuck probe: QProcess emits no exited signal when wl-paste is
    // missing and QML has no error to catch, so the flag is reset by a timer.
    Timer {
        id: watchdog

        interval: 1500
        repeat: false
        onTriggered: root.fallbackToText()
    }

    Process {
        id: typesProc

        command: ["wl-paste", "--list-types"]

        stdout: StdioCollector {
            onStreamFinished: root.handleTypes(text)
        }
    }

    Process {
        id: uriProc

        command: ["wl-paste", "--no-newline", "--type", "text/uri-list"]

        stdout: StdioCollector {
            onStreamFinished: root.handleUriList(text)
        }
    }

    Process {
        id: imageProc

        onExited: (code, status) => {
            if (!root._pending)
                return;
            if (code !== 0) {
                root.fallbackToText();
                return;
            }
            root.settle();
            root.filesFound([root._imagePath]);
        }
    }
}
