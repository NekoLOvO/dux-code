import QtQuick
import qs.Common
import qs.DCommon.Widgets

// Circular icon badge.
Rectangle {
    id: root

    property string iconName: ""
    property real size: Theme.iconButtonSize
    property real iconSize: Theme.iconSizeMedium
    property color containerColor: Theme.withAlpha(Theme.primary, Theme.tonalTintAlpha)
    property color contentColor: Theme.primary

    width: size
    height: size
    radius: Theme.fullRadius(width, height)
    color: containerColor

    Behavior on color {
        enabled: Theme.animationsEnabled

        ColorAnimation {
            duration: Theme.expressiveDurations.expressiveEffects
            easing.type: Easing.BezierSpline
            easing.bezierCurve: Theme.expressiveCurves.expressiveEffects
        }
    }

    DIcon {
        anchors.centerIn: parent
        name: root.iconName
        size: root.iconSize
        color: root.contentColor
    }
}
