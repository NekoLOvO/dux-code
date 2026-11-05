import QtQuick
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets

// QuestionDialog — shown when the model asks the user something (the ask_user tool).
//
// The options are shortcuts: there is always a free-text field next to them, because the
// canned answer is rarely the sentence the user wants. There is no "submit the whole form"
// concept — clicking an option is the answer.
//
// Unlike the permission dialog, Esc or a click outside means "no answer" rather than a
// refusal: the model is told to pick a path itself instead of waiting. So there is no
// timeout: the user answers, skips, or stops the turn.
//
// "Hide" is not "skip". The daemon is blocked on this answer, so putting the dialog aside has to
// leave the question pending — the model may be asking precisely because its analysis above is worth
// reading first. Hiding closes the window and puts the way back in the ask_user tool card, which is
// where the run that is blocked on this answer already shows.
DankOverlayDialog {
    id: root

    readonly property var requests: Backend.questions
    readonly property var current: requests.length > 0 ? requests[0] : null
    readonly property var options: current ? (current.options || []) : []

    title: I18n.tr("Question", "question dialog title")
    maximumWidth: 640

    function _settle(answer) {
        if (!current)
            return;
        Backend.answerQuestion(current.rid, answer);
        // Stay open while more questions are queued; closing and reopening flickers.
        if (Backend.questions.length === 0)
            root.close();
    }

    function _submit() {
        const text = field.text.trim();
        if (text === "")
            return;
        root._settle(text);
    }

    function _openIfPending() {
        // Chat.questionHidden means the user put this question aside on purpose; reopening it on the
        // next event would take that decision back. A *new* question clears the flag in Chat.
        if (Backend.questions.length > 0 && !root.opened && !Chat.questionHidden)
            root.open();
    }

    // Put the question aside without answering it.
    function minimize() {
        if (!current || !opened)
            return;
        Chat.questionHidden = true;
        root.close();
    }

    // Bring it back, from the button in the ask_user tool card.
    function restore() {
        if (!current)
            return;
        Chat.questionHidden = false;
        root.open();
    }

    onOpenedChanged: {
        if (opened)
            field.forceActiveFocus();
    }

    onCurrentChanged: {
        field.text = "";
        if (opened)
            field.forceActiveFocus();
    }

    Component.onCompleted: _openIfPending()

    Connections {
        target: Backend

        function onQuestionPending() {
            root._openIfPending();
        }
    }

    // The way back in: the ask_user tool card, for a question the user put aside.
    Connections {
        target: Chat

        function onQuestionRestoreRequested() {
            root.restore();
        }
    }

    // Esc / close: still "no answer", which the model reads as "you decide". Hiding is the button.
    onRejected: root._settle("")

    Column {
        width: parent.width
        spacing: Theme.spacingM

        Text {
            width: parent.width
            text: root.current ? root.current.question : ""
            color: Theme.surfaceText
            font.pixelSize: Theme.fontSizeMedium
            wrapMode: Text.WordWrap
        }

        Column {
            width: parent.width
            spacing: Theme.spacingS
            visible: root.options.length > 0

            Repeater {
                model: root.options

                delegate: Rectangle {
                    id: optionRow

                    width: parent.width
                    height: optionCol.implicitHeight + Theme.spacingM * 2
                    radius: Theme.cornerRadiusS
                    color: optionHover.hovered ? Theme.surfaceContainerHigh : Theme.chipSurfaceNested
                    border.width: 1
                    border.color: optionHover.hovered ? Theme.primary : Theme.outlineLight

                    Column {
                        id: optionCol

                        anchors.left: parent.left
                        anchors.right: parent.right
                        anchors.verticalCenter: parent.verticalCenter
                        anchors.leftMargin: Theme.spacingM
                        anchors.rightMargin: Theme.spacingM
                        spacing: Theme.spacingXS

                        Text {
                            width: parent.width
                            text: modelData.label || ""
                            color: Theme.surfaceText
                            font.pixelSize: Theme.fontSizeSmall
                            font.weight: Theme.fontWeightMedium
                            elide: Text.ElideRight
                        }

                        Text {
                            width: parent.width
                            visible: text !== ""
                            text: (modelData && modelData.description) ? modelData.description : ""
                            color: Theme.surfaceVariantText
                            font.pixelSize: Theme.fontSizeSmall
                            wrapMode: Text.WordWrap
                        }
                    }

                    HoverHandler {
                        id: optionHover
                        cursorShape: Qt.PointingHandCursor
                    }

                    TapHandler {
                        onTapped: root._settle(modelData.label)
                    }
                }
            }
        }

        Row {
            width: parent.width
            spacing: Theme.spacingS

            DTextField {
                id: field

                width: parent.width - sendButton.width - parent.spacing
                anchors.verticalCenter: parent.verticalCenter
                placeholderText: I18n.tr("Or type your own answer", "question dialog free text placeholder")
                onAccepted: root._submit()
            }

            DButton {
                id: sendButton

                text: I18n.tr("Send", "question dialog send button")
                anchors.verticalCenter: parent.verticalCenter
                onClicked: root._submit()
            }
        }
    }

    actions: [
        DButton {
            text: I18n.tr("Skip — you decide", "question dialog skip button")
            variant: "tonal"
            onClicked: root._settle("")
        },
        DButton {
            // The point of this one: read the reply above, then come back. It does not answer, and the
            // question stays pending in the composer.
            text: I18n.tr("Hide", "question dialog button that puts the question aside without answering")
            iconName: "visibility_off"
            onClicked: root.minimize()
        }
    ]
}
