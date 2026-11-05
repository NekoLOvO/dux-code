import QtQuick
import qs.Common
import qs.DCommon.Common
import qs.DCommon.Widgets

Item {
    id: root

    Column {
        anchors.centerIn: parent
        width: Math.min(root.width - Theme.spacingXL * 4, 660)
        spacing: Theme.spacingM

        StyledText {
            width: parent.width
            horizontalAlignment: Text.AlignHCenter
            text: "Code"
            color: Theme.surfaceText
            font.pixelSize: Theme.fontSizeXXLarge
            font.weight: Font.DemiBold
        }

        StyledText {
            width: parent.width
            horizontalAlignment: Text.AlignHCenter
            text: I18n.tr("Native coding agent for the shell", "empty state tagline")
            color: Theme.surfaceVariantText
            font.pixelSize: Theme.fontSizeMedium
            wrapMode: Text.Wrap
        }
    }
}
