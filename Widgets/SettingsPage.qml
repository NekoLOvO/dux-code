import QtQuick
import qs.Common
import qs.DCommon.Widgets

DFlickable {
    id: root

    default property alias content: column.data
    property alias spacing: column.spacing
    property real contentMaxWidth: SettingsMetrics.contentMaxWidth

    anchors.fill: parent
    clip: true
    contentHeight: column.height + Theme.spacingXL
    contentWidth: width

    // Scroll a row into view and flash a frame around it. Used after jumping from a search hit, where
    // landing on the page but not at the row means hunting for it.
    function revealRow(row) {
        if (!row)
            return;
        // The row's position inside this page's content column, not inside its card.
        const point = row.mapToItem(column, 0, 0);
        reveal.row = row;
        revealTimer.start();
        contentY = Math.max(0, point.y - height / 2 + row.height / 2);
    }

    readonly property Timer revealTimer: Timer {
        id: revealTimer
        interval: SettingsMetrics.flashHold
        onTriggered: reveal.row = null
    }

    Rectangle {
        id: reveal

        property var row: null

        readonly property Item target: row

        visible: target !== null
        x: target ? target.mapToItem(column, 0, 0).x - Theme.spacingXS : 0
        y: target ? target.mapToItem(column, 0, 0).y - Theme.spacingXS : 0
        width: target ? target.width + Theme.spacingXS * 2 : 0
        height: target ? target.height + Theme.spacingXS * 2 : 0
        radius: Theme.cornerRadiusM
        color: "transparent"
        border.width: Theme.focusRingWidth
        border.color: Theme.primary
        z: 100
    }

    Column {
        id: column
        topPadding: Theme.spacingXS
        width: Math.min(root.contentMaxWidth, parent.width)
        bottomPadding: SettingsMetrics.pagePaddingV
        anchors.horizontalCenter: parent.horizontalCenter
        spacing: Theme.spacingL
    }
}
