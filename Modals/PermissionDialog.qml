import QtQuick
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets

// PermissionDialog — the daemon asks here before a tool touches files or runs a command.
//
// Enter and onAccepted are deliberately left unwired: the default action must be "do nothing".
// A dialog that lets a stray Enter through to a shell command is worse than no dialog.
DankOverlayDialog {
    id: root

    readonly property var requests: Backend.permissionRequests
    readonly property var current: requests.length > 0 ? requests[0] : null
    // shell is the only tool not confined to the working directory.
    readonly property bool isShell: current ? current.kind === "bash" : false
    // MCP tools come from an external server: only the name is known, not what it does.
    readonly property bool isMCP: current ? current.kind === "mcp" : false
    // Browser: submits forms and presses buttons, i.e. acts as the user on a site.
    readonly property bool isBrowser: current ? current.kind === "browser" : false
    readonly property string kindLabel: {
        if (!current)
            return "";
        switch (current.kind) {
        case "bash":
            return I18n.tr("Shell command", "permission dialog shell kind label");
        case "mcp":
            return I18n.tr("MCP tool", "permission dialog mcp kind label");
        case "browser":
            return I18n.tr("Browser action", "permission dialog browser kind label");
        default:
            return I18n.tr("Modify file", "permission dialog write kind label");
        }
    }
    readonly property int queueLength: requests.length

    title: current ? current.title : ""
    maximumWidth: 640

    function _settle(allow, reason) {
        if (!current)
            return;
        Backend.answerPermission(current.rid, allow, reason);
        // Stay open while more requests are queued; back-to-back tool calls are common and
        // closing/reopening flickers.
        if (Backend.permissionRequests.length === 0)
            root.close();
    }

    function _openIfPending() {
        if (Backend.permissionRequests.length > 0 && !root.opened)
            root.open();
    }

    Component.onCompleted: _openIfPending()

    Connections {
        target: Backend

        function onPermissionPending() {
            root._openIfPending();
        }
    }

    // Esc or a click outside counts as a refusal, but an answer must still be sent — otherwise
    // the tool on the daemon side waits until it times out.
    onRejected: {
        if (root.opened)
            root.close();
        root._settle(false, "User closed the dialog");
    }

    Column {
        width: parent.width
        spacing: Theme.spacingM

        Row {
            width: parent.width
            spacing: Theme.spacingS

            DIcon {
                name: root.isShell ? "terminal" : (root.isMCP ? "extension" : "edit_document")
                width: 20
                height: 20
                color: root.isShell ? Theme.warning : Theme.primary
                anchors.verticalCenter: parent.verticalCenter
            }

            Text {
                width: parent.width - 20 - Theme.spacingS
                text: root.kindLabel
                color: root.isShell ? Theme.warning : Theme.onSurfaceVariant
                font.pixelSize: Theme.fontSizeSmall
                anchors.verticalCenter: parent.verticalCenter
            }
        }

        // The command or diff verbatim. This block is the only evidence the user has, so it is
        // never truncated.
        Rectangle {
            width: parent.width
            height: Math.min(detailText.contentHeight + Theme.spacingM * 2, Theme.spacingXL * 18)
            radius: Theme.cornerRadiusS
            color: Theme.chipSurfaceNested
            border.width: 1
            border.color: Theme.outlineLight
            clip: true

            DFlickable {
                id: detailScroll

                anchors.fill: parent
                anchors.margins: Theme.spacingS
                clip: true
                contentWidth: width
                contentHeight: detailText.contentHeight

                // TextEdit rather than Text: the content is a command or a diff and the user
                // wants to copy a piece out to check it, so it has to be selectable.
                TextEdit {
                    id: detailText

                    width: detailScroll.width - (Theme.spacingS)
                    text: root.current ? root.current.detail : ""
                    color: Theme.surfaceText
                    font.family: Theme.defaultMonoFontFamily
                    font.pixelSize: Theme.fontSizeSmall
                    wrapMode: TextEdit.WrapAnywhere
                    textFormat: TextEdit.PlainText
                    readOnly: true
                    selectByMouse: true
                    selectionColor: Theme.primary
                    selectedTextColor: Theme.primaryText
                }
            }
        }

        Text {
            width: parent.width
            visible: root.isShell
            text: I18n.tr("This runs with your full user permissions — it is not limited to the working directory.",
                          "permission dialog shell warning")
            color: Theme.onSurfaceVariant
            font.pixelSize: Theme.fontSizeSmall
            wrapMode: Text.WordWrap
        }

        Text {
            width: parent.width
            visible: root.queueLength > 1
            text: I18n.tr("%1 more waiting", "permission dialog queued count").arg(root.queueLength - 1)
            color: Theme.onSurfaceVariant
            font.pixelSize: Theme.fontSizeSmall
        }
    }

    actions: [
        DButton {
            text: I18n.tr("Deny", "permission dialog deny button")
            variant: "tonal"
            onClicked: root._settle(false, "")
        },
        DButton {
            text: I18n.tr("Allow", "permission dialog allow button")
            // A shell command is the dangerous one, so it takes the error colour.
            variant: root.isShell ? "tonal" : "filled"
            danger: root.isShell
            onClicked: root._settle(true, "")
        }
    ]
}
