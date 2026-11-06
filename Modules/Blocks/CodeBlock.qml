import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.DCommon.Common
import qs.DCommon.Widgets
import qs.Widgets
import "../../Common/TextFmt.js" as TextFmt

Item {
    id: root

    property var blk: ({})
    // Inside thinking blocks: one step smaller header and type scale so the block stays background.
    property bool muted: false

    // Highlighted HTML. Empty means "not ready yet" or "not worth highlighting", and the
    // raw text is shown instead. Skipped while streaming, which would re-analyze the block
    // every few characters.
    property string html: ""

    readonly property bool closed: blk && blk.closed === true

    function refreshHighlight() {
        if (!closed || displayCode === "") {
            html = "";
            return;
        }
        Backend.highlightCode(lang, displayCode, Theme.isLightMode, (out, err) => {
            // Content may have changed by the time this returns (regenerate, session
            // switch); never overwrite with a stale result.
            if (err || !out || displayCode !== root.displayCode || lang !== root.lang)
                return;
            html = out;
        });
    }

    // Binding blk flips code / lang / closed one after another, so sending immediately
    // would fire two or three times; wait 50 ms for it to settle.
    Timer {
        id: hlTimer
        interval: 50
        onTriggered: root.refreshHighlight()
    }

    onClosedChanged: hlTimer.restart()
    onLangChanged: hlTimer.restart()
    onCodeChanged: {
        // Still streaming: plain text now, highlight once the fence closes.
        if (!closed) {
            html = "";
            return;
        }
        // A closed block restored from a session flips closed before code, so the first
        // attempt is skipped while code is still empty; retry here.
        hlTimer.restart();
    }
    Component.onCompleted: hlTimer.start()

    // Light/dark changed, so chroma's colors are stale; recompute.
    Connections {
        target: Theme
        function onIsLightModeChanged() {
            root.refreshHighlight();
        }
    }
    readonly property string lang: (blk && blk.lang) ? blk.lang : ""
    readonly property string code: (blk && blk.code) ? blk.code : ""
    // Display form: tabs become four spaces. The original (root.code) is what gets copied.
    readonly property string displayCode: TextFmt.expandTabs(code)

    implicitHeight: card.height

    // Card is the library's DCard with pad 0, so header, divider and bottom progress bar
    // touch the edges.
    DCard {
        id: card
        width: parent.width
        height: head.height + body.height
        pad: 0
        radius: Theme.cornerRadiusM
        color: Theme.surfaceContainerHigh
        border.width: Theme.outlineWidth
        border.color: root.closed ? Theme.outlineVariant : Theme.primary

        Item {
            id: head
            width: parent.width
            height: root.muted ? 32 : 40

            StyledText {
                anchors.left: parent.left
                anchors.leftMargin: Theme.spacingM
                anchors.verticalCenter: parent.verticalCenter
                text: root.lang.length > 0 ? root.lang : "text"
                color: Theme.surfaceVariantText
                font.pixelSize: Theme.fontSizeSmall
                isMonospace: true
            }

            CopyButton {
                anchors.right: parent.right
                anchors.rightMargin: Theme.spacingXS
                anchors.verticalCenter: parent.verticalCenter
                copyText: root.code
            }
        }

        Rectangle {
            anchors.top: head.bottom
            anchors.left: parent.left
            anchors.right: parent.right
            height: Theme.dividerWidth
            color: Theme.outlineVariant
        }

        Flickable {
            id: body
            anchors.top: head.bottom
            width: parent.width
            height: edit.height + Theme.spacingM
            contentWidth: Math.max(edit.implicitWidth + Theme.spacingL * 2, width)
            contentHeight: height
            clip: true
            boundsBehavior: Flickable.StopAtBounds

            TextEdit {
                id: edit
                x: Theme.spacingM
                y: Theme.spacingXS
                width: Math.max(implicitWidth, body.width - Theme.spacingL * 2)
                readOnly: true
                selectByMouse: true
                textFormat: root.html !== "" ? TextEdit.RichText : TextEdit.PlainText
                wrapMode: TextEdit.NoWrap
                color: Theme.surfaceText
                selectionColor: Theme.primary
                selectedTextColor: Theme.primaryText
                renderType: Text.NativeRendering
                font.family: Theme.monoFontFamily
                font.pixelSize: root.muted ? Theme.fontSizeSmall : Theme.fontSizeSmall + 1
                // Rich text consumes HTML wrapped in <pre>; plain text when unchecked.
                text: root.html !== "" ? root.html : root.displayCode

                // One selection at a time, shared with the prose blocks around this card.
                onSelectedTextChanged: if (selectedText !== "") Selection.claim(edit)
                onActiveFocusChanged: if (activeFocus) Selection.claim(edit)
                Component.onDestruction: Selection.release(edit)
            }
        }

        Rectangle {
            anchors.bottom: parent.bottom
            anchors.left: parent.left
            anchors.right: parent.right
            height: 2
            radius: 1
            visible: !root.closed
            color: Theme.primary

            DBlink {
                id: progressPulse
                target: parent
                running: !root.closed
                minOpacity: 0.15
                pulseDuration: 700
            }
        }
    }
}
