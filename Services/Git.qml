pragma Singleton
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import qs.Common
import qs.Services

// Git state for one working directory, as the composer's chip draws it.
//
// The daemon answers because that is where the working directory lives and because `git status` blocks:
// on a large tree it is hundreds of milliseconds, which is not something to do on the UI thread while a
// reply streams. Nothing here is fatal — a directory that is not a repository leaves `repo` false and
// the chip does not exist.
//
// The directory is passed in on every call rather than read from a setting here, because the composer
// shows `SettingsData.workingDirectory` with a fallback to $HOME, and the chip has to describe exactly
// what that field says — including in a session whose stored directory differs.
Singleton {
    id: git

    // Last answer for the directory below. Empty until the first reply.
    property var status: ({})
    // False until the daemon has answered once, so "not asked yet" and "not a repository" stay distinct.
    property bool loaded: false
    property string error: ""
    // The directory `status` describes.
    property string dir: ""

    readonly property bool repo: status.repo === true
    readonly property string branch: status.branch || ""
    readonly property bool detached: status.detached === true
    readonly property int ahead: status.ahead || 0
    readonly property int behind: status.behind || 0
    readonly property int dirty: status.dirty || 0
    readonly property var files: status.files || []

    // One request in flight, one waiting behind it: switching directories twice in a row must end with
    // the newest answer, not whichever `git status` finished last. `_target` is that newest answer's
    // directory — a reply for anything else is dropped.
    property string _target: ""
    property string _queued: ""
    property bool _busy: false

    function request(directory) {
        if (directory === undefined || directory === "")
            return;
        _target = directory;
        _send(directory);
    }

    function _send(directory) {
        if (_busy) {
            _queued = directory;
            return;
        }
        _busy = true;
        Backend.sendRequest("git.status", { "dir": directory }, (res, err) => {
            git._busy = false;
            if (directory === git._target) {
                git.dir = directory;
                git.loaded = true;
                git.error = err ? String(err) : "";
                git.status = git.error !== "" ? ({ "error": git.error }) : (res && res.status ? res.status : ({}));
            }
            if (git._queued !== "") {
                const next = git._queued;
                git._queued = "";
                git._send(next);
            }
        });
    }

    // branches fills a menu. Local branches only; switching to a remote one means creating a local
    // tracking branch first, which is a name the user has to choose.
    function branches(directory, callback) {
        Backend.sendRequest("git.branches", { "dir": directory }, (res, err) => {
            if (callback)
                callback(res && res.branches ? res.branches : [], err ? String(err) : "");
        });
    }

    // checkout switches branch and hands back either nothing (success) or git's own complaint, which the
    // caller shows verbatim: "Your local changes to the following files would be overwritten" tells the
    // user more than any paraphrase, and it carries the file list.
    function checkout(directory, branch, callback) {
        Backend.sendRequest("git.checkout", { "dir": directory, "branch": branch }, (res, err) => {
            if (err) {
                if (callback)
                    callback(String(err));
                return;
            }
            // The reply carries the new state, so the chip updates without a second round trip.
            if (directory === _target && res && res.status) {
                status = res.status;
                loaded = true;
                error = res.status.error || "";
            }
            if (callback)
                callback("");
        });
    }
}
