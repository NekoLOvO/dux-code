import QtQuick
import qs.Common

Item {
    id: root
    property var blk: ({})
    implicitHeight: Theme.spacingL

    Rectangle {
        anchors.verticalCenter: parent.verticalCenter
        width: parent.width
        height: Theme.dividerWidth
        color: Theme.outlineVariant
    }
}
