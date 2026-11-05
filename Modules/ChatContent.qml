import QtQuick
import QtQuick.Controls
import Quickshell
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets

// Conversation area. Toolbar follows CalendarContent's layout (hamburger + title + actions);
// the calendar body is replaced by the message stream, with the composer at the bottom.
Item {
    id: root

    property bool menuButtonVisible: false
    property string title: Sessions.activeTitle

    // ChatWindow, for the composer's file pickers — see Composer.hostWindow.
    property var hostWindow: null

    signal menuRequested
    signal settingsRequested

    Column {
        anchors.fill: parent
        spacing: 0

        // --- toolbar ---
        Item {
            id: toolbar
            width: parent.width
            height: Theme.buttonHeightM

            Row {
                id: navigationRow
                anchors.left: parent.left
                anchors.leftMargin: Theme.spacingL
                anchors.verticalCenter: parent.verticalCenter
                spacing: Theme.spacingS

                DActionButton {
                    visible: root.menuButtonVisible
                    iconName: "menu"
                    iconColor: Theme.surfaceText
                    buttonSize: Theme.buttonHeightS
                    focusPolicy: Qt.NoFocus
                    Accessible.name: I18n.tr("Toggle sidebar", "toolbar button that shows or hides the sidebar")
                    anchors.verticalCenter: parent.verticalCenter
                    onClicked: root.menuRequested()
                }
            }

            StyledText {
                id: titleText
                anchors.left: navigationRow.right
                anchors.leftMargin: Theme.spacingM
                anchors.right: actionsRow.left
                anchors.rightMargin: Theme.spacingM
                anchors.verticalCenter: parent.verticalCenter
                text: root.title
                font.pixelSize: Theme.fontSizeLarge
                font.weight: Theme.fontWeightMedium
                color: Theme.surfaceText
                elide: Text.ElideRight
                maximumLineCount: 1
                wrapMode: Text.NoWrap
            }

            Row {
                id: actionsRow
                anchors.right: parent.right
                anchors.rightMargin: Theme.spacingL
                anchors.verticalCenter: parent.verticalCenter
                spacing: Theme.spacingXS

                // Back to top: appears once scrolled away. The scrollbar auto-hides in long
                // threads, so wheel-scrolling back is painful. MessageList.scrollToTop() clears
                // stick-to-end first, or the pending stick would pull us back immediately.
                DActionButton {
                    visible: !list.atTop
                    iconName: "vertical_align_top"
                    iconColor: Theme.surfaceVariantText
                    buttonSize: Theme.buttonHeightS
                    focusPolicy: Qt.NoFocus
                    Accessible.name: I18n.tr("Scroll to top", "toolbar button that jumps the conversation back to the top")
                    onClicked: list.scrollToTop()
                }

                // No copy-all: every message has its own copy button, and this one only
                // invites mistaps.
                DActionButton {
                    iconName: "settings"
                    iconColor: Theme.surfaceVariantText
                    buttonSize: Theme.buttonHeightS
                    focusPolicy: Qt.NoFocus
                    Accessible.name: I18n.tr("Settings", "toolbar button that opens settings")
                    onClicked: root.settingsRequested()
                }
            }
        }

        // --- messages ---
        Item {
            id: body
            width: parent.width
            height: parent.height - toolbar.height - composer.height

            MessageList {
                id: list
                anchors.fill: parent
                anchors.topMargin: Theme.spacingM

                onEditRequested: (index, text) => root.beginEdit(index, text)
                onRegenerateRequested: index => Chat.regenerate(index)
                onBranchRequested: index => Chat.branch(index)
                // Continue is a new user message: the model sees its own half-finished sentence
                // and picks up from there. Appending the text in place would glue two model
                // outputs into one message and break edit/branch.
                onContinueRequested: index => {
                    if (!Chat.busy)
                        Chat.send(I18n.tr("Continue", "message sent when the user asks a truncated reply to keep going"));
                }
            }

            EmptyState {
                anchors.fill: parent
                visible: Chat.messages.count === 0
            }
        }

        // --- composer ---
        Composer {
            id: composer
            width: parent.width
            hostWindow: root.hostWindow
            onSubmitted: function (text, attachments) {
                Chat.send(text, attachments);
            }
        }
    }

    // --- editing an earlier message ---
    // Same dialog as the full-window editor; on accept it overwrites that message, drops
    // everything after it and regenerates.
    property int editIndex: -1

    function beginEdit(index, text) {
        editIndex = index;
        editField.text = text;
        editDialog.open();
        Qt.callLater(() => {
            editField.forceActiveFocus();
            editField.cursorPosition = editField.text.length;
        });
    }

    function commitEdit() {
        var t = editField.text;
        if (t.trim() === "")
            return;
        var idx = editIndex;
        editIndex = -1;
        editDialog.close();
        Chat.editMessage(idx, t);
    }

    DankOverlayDialog {
        id: editDialog

        title: I18n.tr("Edit message", "dialog title for editing an earlier prompt")
        maximumWidth: 1000

        onAccepted: root.commitEdit()
        onRejected: editDialog.close()

        // Eight fixed lines that scroll past the last one; width follows the dialog's
        // content column so a long prompt wraps instead of overflowing horizontally.
        ComposerField {
            id: editField

            minimumLines: 8
            maximumLines: 8
            backgroundColor: "transparent"
            borderWidth: 0
            focusedBorderWidth: 0
            contentPadding: 0
            verticalPadding: 0
            textColor: Theme.surfaceText
            font.pixelSize: Theme.fontSizeLarge
            onAccepted: root.commitEdit()
            onEscapePressed: editDialog.close()
        }

        actions: [
            DButton {
                text: I18n.tr("Cancel", "edit dialog cancel button")
                variant: "tonal"
                onClicked: editDialog.close()
            },
            DButton {
                text: I18n.tr("Save & regenerate", "edit dialog button that rewrites the prompt and re-runs the model")
                enabled: editField.text.trim().length > 0 && !Chat.busy
                onClicked: root.commitEdit()
            }
        ]
    }
}
