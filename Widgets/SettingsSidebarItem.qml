import QtQuick
import qs.Common
import qs.DCommon.Widgets

// Settings navigation item built on the library's DListItem: hover layer, ripple,
// focus ring, grouped radii and keyboard focus come with it.
DListItem {
    id: root

    property string iconName: ""
    property string title: ""
    property string hint: ""
    property string accent: ""
    property bool active: false
    property bool highlighted: false
    // Hits on this page while searching; -1 hides the count.
    property int matchCount: -1

    readonly property var accentPair: Theme.accent(accent)
    readonly property color badgeColor: accentPair?.container ?? Theme.withAlpha(Theme.primary, Theme.tonalTintAlpha)
    readonly property color glyphColor: accentPair?.onContainer ?? Theme.primary

    width: parent?.width ?? 0
    height: Math.max(SettingsMetrics.navItemMinHeight, textColumn.implicitHeight + Theme.spacingS * 2)
    surfaceColor: SettingsMetrics.sidebarRowColor
    isSelected: active
    border.width: 0
    // Focusable so Tab walks the items.
    focusPolicy: Qt.StrongFocus
    color: {
        if (active)
            return SettingsMetrics.selectedRowColor;
        if (highlighted)
            return Theme.blend(SettingsMetrics.sidebarRowColor, Theme.primary, SettingsMetrics.highlightBlend);
        return SettingsMetrics.sidebarRowColor;
    }

    Behavior on color {
        enabled: Theme.animationsEnabled
        ColorAnimation {
            duration: SettingsMetrics.fadeDuration
            easing.type: Easing.BezierSpline
            easing.bezierCurve: Theme.expressiveCurves.expressiveEffects
        }
    }

    IconBadge {
        id: iconCircle

        anchors.left: parent.left
        anchors.leftMargin: Theme.spacingL
        anchors.verticalCenter: parent.verticalCenter
        size: SettingsMetrics.navIconSize
        iconName: root.iconName
        containerColor: root.badgeColor
        contentColor: root.glyphColor
    }

    Column {
        id: textColumn
        anchors.left: iconCircle.right
        anchors.leftMargin: Theme.spacingM
        anchors.right: parent.right
        anchors.rightMargin: Theme.spacingL
        anchors.verticalCenter: parent.verticalCenter
        spacing: Theme.spacingXXS

        StyledText {
            width: parent.width
            text: root.title
            font.pixelSize: Theme.fontSizeMedium
            font.weight: Theme.fontWeightMedium
            color: Theme.surfaceText
            elide: Text.ElideRight
            horizontalAlignment: Text.AlignLeft
        }

        StyledText {
            width: parent.width
            text: root.matchCount > 0
                ? (root.matchCount === 1 ? I18n.tr("1 match", "settings sidebar tab while searching: one result") : I18n.tr("%1 matches", "settings sidebar tab while searching: result count").arg(root.matchCount))
                : root.hint
            font.pixelSize: Theme.fontSizeSmall
            color: root.matchCount > 0 ? Theme.primary : Theme.surfaceVariantText
            elide: Text.ElideRight
            visible: root.matchCount > 0 || root.hint !== ""
            horizontalAlignment: Text.AlignLeft
        }
    }

}
