pragma Singleton

import QtQuick
import Quickshell
import qs.Common

Singleton {
    readonly property real sidebarWidth: 260
    readonly property real compactBreakpoint: 640
    readonly property real contentMaxWidth: Number.POSITIVE_INFINITY
    readonly property real pagePaddingV: Theme.spacingXL + Theme.densityStepM
    readonly property real pageHeaderHeight: Theme.fontSizeXXLarge + Theme.spacingXL + Theme.spacingM
    readonly property real paneMargin: Theme.windowInset
    readonly property real panePadding: Theme.spacingXL
    readonly property real paneRadius: Theme.cornerRadiusL
    readonly property real rowPaddingH: Theme.spacingL + Theme.spacingXS + Theme.densityStepS
    readonly property real rowPaddingV: Theme.spacingL + Theme.densityStepS
    readonly property real rowMinHeight: Math.max(Theme.listItemHeight + Theme.densityStepM, Theme.buttonHeightS + Theme.spacingXS)
    readonly property real rowMinHeightTwoLine: Theme.listItemTwoLineHeight + Theme.densityStepM
    readonly property real cardGap: Theme.spacingL + Theme.densityStepS
    readonly property real rowContentSpacing: Theme.spacingL
    readonly property real sectionLabelTopGap: Math.max(Theme.spacingXS, Theme.spacingS + Theme.densityStepS)
    readonly property real sectionLabelBottomGap: Theme.spacingM
    readonly property real navIconSize: Theme.avatarSize
    readonly property real navItemMinHeight: rowMinHeight
    readonly property real sidebarGroupGap: Theme.spacingS
    readonly property real buttonGroupCompactThreshold: 200
    readonly property real disabledOpacity: 0.38
    readonly property real highlightBlend: 0.2
    readonly property color paneColor: Theme.floatingWindowPaneSurface
    readonly property color sidebarRowColor: Theme.floatingWindowPaneSurface
    readonly property color rowColor: Theme.foregroundColor(Theme.chipSurface, true)
    readonly property color controlSurface: Theme.chipSurfaceNested
    readonly property color selectedRowColor: Theme.blend(rowColor, Theme.onSurface, Theme.stateLayerDrag)
    readonly property int transitionDuration: Theme.expressiveDurations.expressiveFastSpatial
    readonly property int fadeDuration: Theme.expressiveDurations.expressiveEffects
    // How long a row stays outlined after a search jump: long enough to find it, short enough not to
    // become part of the page.
    readonly property int flashHold: 1600
}
