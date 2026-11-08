import QtQuick
import qs.Common
import qs.Services
import qs.DCommon.Common
import qs.DCommon.Widgets

// Branch, sync state and a dirty count for the directory the composer will run in.
//
// It lives above the field rather than in the toolbar: the toolbar is full (attach, expand, search,
// send on the right with the model, reasoning and token chips), and this answers the same question the
// folder chip next to it does — where does this turn run. Picking a branch is a menu, not a dialog:
// it closes on Escape and on a click outside, both of which DDropdown already does.
//
// Nothing is drawn outside a repository. A coding agent spends time in plain directories too, and a
// chip saying "no repository" would be noise on every one of them.
Item {
    id: root

    // The work tree to describe. Set by the composer from the same value its folder chip shows.
    property string dir: ""

    readonly property var s: Git.status
    readonly property bool known: Git.loaded && Git.dir === dir && Git.repo
    readonly property string errorText: Git.error

    visible: known
    implicitWidth: chip.implicitWidth
    implicitHeight: chip.implicitHeight

    onDirChanged: Git.request(dir)
    Component.onCompleted: Git.request(dir)

    // The window is created before the socket is up, so the first request lands on "daemon not
    // connected" and the chip would stay hidden for the whole session. This is the second entry point;
    // Sessions and Skills have the same one for the same reason.
    Connections {
        target: Backend

        function onConnectedChanged() {
            if (Backend.connected)
                Git.request(root.dir);
        }
    }

    // A turn just ran somewhere in this tree: the agent may have committed, staged or switched. Asking
    // git after the fact is cheaper than watching the filesystem, and this is the only moment the state
    // is expected to have moved.
    Connections {
        target: Chat

        function onBusyChanged() {
            if (!Chat.busy && root.visible)
                Git.request(root.dir);
        }
    }

    // What the chip says. Markers are text rather than icons: three icon components plus spacing would
    // cost more width than the branch name itself on a narrow window.
    readonly property string label: {
        let text = Git.branch;
        if (Git.ahead > 0)
            text += " ↑" + Git.ahead;
        if (Git.behind > 0)
            text += " ↓" + Git.behind;
        if (Git.dirty > 0)
            text += " ●" + Git.dirty;
        return text;
    }

    readonly property string tooltip: {
        if (root.errorText !== "")
            return root.errorText;
        const lines = [];
        lines.push(Git.detached
            ? I18n.tr("Detached HEAD at %1", "git chip tooltip, %1 is a commit hash").arg(Git.branch)
            : I18n.tr("On branch %1", "git chip tooltip, %1 is a branch name").arg(Git.branch));
        if (Git.status.upstream)
            lines.push(I18n.tr("%1 ahead, %2 behind %3", "git chip tooltip: commits ahead, commits behind, upstream ref").arg(Git.ahead).arg(Git.behind).arg(Git.status.upstream));
        if (Git.dirty > 0) {
            lines.push(Git.dirty === 1
                ? I18n.tr("1 changed file", "git chip tooltip")
                : I18n.tr("%1 changed files", "git chip tooltip, %1 is a count").arg(Git.dirty));
            // The list is capped by the daemon at 20; the tooltip shows fewer so it stays a tooltip.
            for (let i = 0; i < Math.min(Git.files.length, 8); i++)
                lines.push("  " + Git.files[i]);
            if (Git.files.length > 8)
                lines.push("  …");
        }
        return lines.join("\n");
    }

    // The menu is a DDropdown with no trigger of its own, anchored to the chip: the popup, keyboard
    // handling and click-outside dismissal are all work that is already done there, and re-drawing them
    // for one more menu would be a fourth copy in this tree.
    property var branches: []

    DDropdown {
        id: branchMenu

        // Only the menu, not the field.
        showTrigger: false
        popupAnchorItem: chip
        openUpwards: true
        alignPopupRight: false
        dropdownWidth: 220
        // No search field: a branch list is a handful of names, sorted by recency, so the one wanted is
        // at the top. The field would cost a row of height and take the focus, which means arrow keys no
        // longer move through the list — and the model menu in the same toolbar has none either.
        currentValue: Git.branch
        options: root.branches
        optionValues: root.branches

        onValueChanged: value => {
            if (value === "" || value === Git.branch)
                return;
            Git.checkout(root.dir, value, failure => {
                if (failure !== "") {
                    // git's own sentence, on one line: the first line names the problem, and the rest is
                    // the file list the user can already see in the dirty count.
                    ToastService.info(failure.split("\n")[0]);
                    // The menu changed nothing, so put the field back where the tree actually is.
                    branchMenu.currentValue = Git.branch;
                    return;
                }
                ToastService.info(I18n.tr("Switched to %1", "toast after switching git branch, %1 is the branch name").arg(value));
            });
        }
    }

    // The list is fetched when the menu opens, not with every status: branch names change rarely, and
    // `for-each-ref` is a second process.
    function openBranchMenu() {
        Git.branches(dir, (names, failure) => {
            if (failure !== "") {
                ToastService.info(failure.split("\n")[0]);
                return;
            }
            root.branches = names;
            branchMenu.showDropdownMenu();
        });
    }

    DButton {
        id: chip

        buttonHeight: Theme.buttonHeightXS
        maximumWidth: 240
        iconName: "fork_right"
        iconSize: Theme.iconSizeSmall
        text: root.label
        backgroundColor: root.errorText !== "" ? Theme.errorContainer : Theme.surfaceContainerHighest
        textColor: root.errorText !== "" ? Theme.onErrorContainer : Theme.surfaceVariantText
        tooltipText: root.tooltip
        focusPolicy: Qt.NoFocus

        onClicked: root.openBranchMenu()
    }
}
