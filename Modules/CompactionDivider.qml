import QtQuick
import qs.Common
import qs.DCommon.Common
import qs.DCommon.Widgets

// The line where a compaction takes over.
//
// Everything above it is no longer sent to the model; a summary stands in for it. A number in a
// chip could only say "some messages are gone", which is both alarming and unactionable — the
// useful thing is the boundary itself, and the text that replaced the part above it.
//
// Collapsed by default: it is a seam in the transcript, not content. Expanded it shows the summary,
// because "the model is working from a summary" is only fair to know when a reply ignores something
// said earlier.
Item {
    id: root

    // How many messages the summary replaced (the daemon's count).
    property int count: 0
    property string summary: ""
    property bool open: false

    // One wording for the pill and for assistive technology; a second copy would drift.
    readonly property string label: count === 1
        ? I18n.tr("Summary of 1 earlier message", "compaction seam in the transcript: a summary replaced exactly one message")
        : I18n.tr("Summary of %1 earlier messages", "compaction seam in the transcript: a summary replaced this many messages").arg(count)

    implicitHeight: column.implicitHeight
    height: implicitHeight

    Column {
        id: column

        width: parent.width
        spacing: Theme.spacingS

        // ---- the seam: rule, pill, rule ----
        Item {
            width: parent.width
            height: Theme.buttonHeightXS

            Rectangle {
                anchors.left: parent.left
                anchors.right: pill.left
                anchors.rightMargin: Theme.spacingS
                anchors.verticalCenter: parent.verticalCenter
                height: Theme.dividerWidth
                color: Theme.outlineVariant
            }

            Rectangle {
                anchors.left: pill.right
                anchors.leftMargin: Theme.spacingS
                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
                height: Theme.dividerWidth
                color: Theme.outlineVariant
            }

            Row {
                id: pill

                anchors.centerIn: parent
                spacing: Theme.spacingXS

                StyledText {
                    anchors.verticalCenter: parent.verticalCenter
                    text: "compress"
                    font.family: Fonts.icons
                    font.pixelSize: Theme.iconSizeSmall
                    color: Theme.surfaceVariantText
                }

                StyledText {
                    anchors.verticalCenter: parent.verticalCenter
                    text: root.label
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                }

                StyledText {
                    anchors.verticalCenter: parent.verticalCenter
                    text: root.open ? "expand_less" : "expand_more"
                    font.family: Fonts.icons
                    font.pixelSize: Theme.iconSizeSmall
                    color: Theme.surfaceVariantText
                }
            }

            // A thin hit area over the whole seam, so the rule is part of the button rather than
            // decoration next to it.
            MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                Accessible.role: Accessible.Button
                Accessible.name: root.label
                onClicked: root.open = !root.open
            }
        }

        // ---- the summary itself ----
        Column {
            width: parent.width
            spacing: Theme.spacingXS
            visible: root.open
            height: visible ? implicitHeight : 0

            Rectangle {
                width: parent.width
                height: body.implicitHeight + Theme.spacingM * 2
                radius: Theme.cornerRadiusM
                color: Theme.surfaceContainer
                border.width: Theme.outlineWidth
                border.color: Theme.outlineVariant

                TextEdit {
                    id: body

                    x: Theme.spacingM
                    y: Theme.spacingM
                    width: parent.width - Theme.spacingM * 2
                    readOnly: true
                    selectByMouse: true
                    persistentSelection: true
                    textFormat: TextEdit.MarkdownText
                    wrapMode: TextEdit.Wrap
                    text: root.summary
                    color: Theme.surfaceText
                    selectionColor: Theme.primary
                    selectedTextColor: Theme.primaryText
                    renderType: Text.NativeRendering
                    font.family: Theme.fontFamily
                    font.pixelSize: Theme.fontSizeSmall
                }
            }

            StyledText {
                width: parent.width
                text: I18n.tr("Everything above this line stays in your transcript but is no longer sent to the model.", "compaction seam: explanation shown under the summary")
                font.pixelSize: Theme.fontSizeSmall
                color: Theme.surfaceVariantText
                wrapMode: Text.WordWrap
            }
        }
    }
}
