import QtQuick
import qs.Common
import qs.DCommon.Common
import qs.DCommon.Widgets
import "../../Common/TextFmt.js" as TextFmt

Item {
    id: root

    property var blk: ({})

    readonly property string toolName: (blk && blk.lang) ? blk.lang : "tool"
    readonly property bool running: blk && blk.status === "running"

    implicitHeight: card.height

    DCard {
        id: card
        width: parent.width
        height: col.implicitHeight + pad * 2
        pad: Theme.spacingM
        radius: Theme.cornerRadiusM
        color: Theme.surfaceContainer
        border.width: Theme.outlineWidth
        border.color: root.running ? Theme.primary : Theme.outlineVariant

        Column {
            id: col
            width: parent.width
            spacing: Theme.spacingS

            Item {
                width: parent.width
                height: Math.max(20, statusBadge.implicitHeight)

                Rectangle {
                    id: dot
                    width: 8
                    height: 8
                    radius: 4
                    anchors.verticalCenter: parent.verticalCenter
                    color: root.running ? Theme.primary : Theme.tertiary

                    DBlink {
                        target: dot
                        running: root.running
                        minOpacity: 0.25
                        pulseDuration: 620
                    }
                }

                StyledText {
                    anchors.left: dot.right
                    anchors.leftMargin: Theme.spacingS
                    anchors.verticalCenter: parent.verticalCenter
                    text: root.toolName
                    color: Theme.surfaceText
                    font.pixelSize: Theme.fontSizeSmall
                    isMonospace: true
                }

                DBadge {
                    id: statusBadge

                    anchors.right: parent.right
                    anchors.verticalCenter: parent.verticalCenter
                    text: root.running ? I18n.tr("Working", "tool card badge while running") : I18n.tr("Done", "tool card badge when finished")
                    color: root.running ? Theme.primary : Theme.surfaceContainerHighest
                    textColor: root.running ? Theme.primaryText : Theme.surfaceVariantText
                }
            }

            StyledText {
                width: parent.width
                // Expand tabs: Qt's 80 px tab stop would otherwise skew the output badly.
                text: TextFmt.expandTabs((root.blk && root.blk.args)
                      ? JSON.stringify(root.blk.args)
                      : (root.blk && root.blk.code ? root.blk.code : ""))
                color: Theme.surfaceVariantText
                font.pixelSize: Theme.fontSizeSmall
                isMonospace: true
                wrapMode: Text.WrapAnywhere
            }
        }
    }
}
