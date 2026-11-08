import QtQuick
import Quickshell
import qs.Common
import qs.DCommon.Widgets

// Copy button: copies copyText and swaps the icon for a check mark.
// Built on DIconButton for the shapeMorph press animation; fixed at XS (28 px)
// so it fits inside the 40 px title bar.
DIconButton {
    id: root

    // Text to copy. Empty string does nothing.
    //
    // Not named "text": StyledButton (DActionButton's base) declares it FINAL, so
    // overriding fails at load time with "Cannot override FINAL property".
    property string copyText: ""

    size: "s"
    buttonSize: Theme.buttonHeightXXS
    width: buttonSize
    iconSize: Theme.iconSizeSmall
    iconName: copied ? "check" : "content_copy"
    opacity: copyHover.hovered || copied ? 1 : 0.55

    // Show a check mark briefly; the button has no label, so this is the only feedback.
    property bool copied: false

    onClicked: {
        if (copyText === "")
            return;
        Quickshell.clipboardText = copyText;
        copied = true;
        resetT.restart();
    }

    Timer {
        id: resetT
        interval: 1400
        onTriggered: root.copied = false
    }

    HoverHandler {
        id: copyHover
    }
}
