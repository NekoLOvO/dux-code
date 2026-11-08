import QtQuick
import qs.Common
import qs.Widgets
import qs.DCommon.Widgets

DankOverlayDialog {
    id: root

    property string message: ""
    property string confirmText: I18n.tr("Confirm", "confirm dialog default confirm button")
    property bool danger: false

    signal confirmed

    function show(opts) {
        title = opts.title || "";
        message = opts.message || "";
        confirmText = opts.confirmText || I18n.tr("Confirm", "confirm dialog default confirm button");
        danger = opts.danger === true;
        open();
    }

    function confirm() {
        confirmed();
        close();
    }

    supportingText: message
    onAccepted: confirm()

    actions: [
        DButton {
            text: I18n.tr("Cancel", "confirm dialog button to cancel")
            variant: "tonal"
            onClicked: root.close()
        },
        DButton {
            text: root.confirmText
            // A dangerous confirm is the same button in the error colour: the variant pairs the
            // background with its text, which is the part that went wrong when it was written by hand.
            variant: root.danger ? "tonal" : "filled"
            danger: root.danger
            onClicked: root.confirm()
        }
    ]
}
