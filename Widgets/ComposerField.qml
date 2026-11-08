import QtQuick
import QtQuick.Controls
import qs.Common
import qs.DCommon.Common
import qs.DCommon.Widgets

// Multi-line counterpart of DTextField: same background, border, placeholder and
// focus colors, but wraps, grows with content up to maximumLines, submits on Enter
// and inserts a newline on Shift/Ctrl+Enter.
//
// Past maximumLines the text scrolls instead of being clipped: TextEdit is not a
// Flickable, so it lives inside one. Nothing here may set a height on the editor
// that ignores its content, or the cursor would be able to leave the visible area.
StyledRect {
    id: root

    property alias text: editor.text
    property alias cursorPosition: editor.cursorPosition
    property alias font: editor.font
    property alias textColor: editor.color
    property alias readOnly: editor.readOnly

    property string placeholderText: ""
    property color placeholderColor: Style.onSurfaceVariant
    property bool hidePlaceholderOnFocus: true

    property int minimumLines: 1
    property int maximumLines: 6

    // Same visual language as DTextField.
    property color backgroundColor: Style.chipSurface
    property color normalBorderColor: Style.outlineVariant
    property color focusedBorderColor: Style.primary
    property real borderWidth: Style.outlineWidth
    property real focusedBorderWidth: Style.outlineWidthFocused
    property real cornerRadius: Style.cornerRadiusXS
    property real contentPadding: Style.spacingM
    property real verticalPadding: Style.spacingS

    readonly property int lineHeight: Math.ceil(font.pixelSize * 1.5)
    // Visible height of the text area: enough for minimumLines, never more than
    // maximumLines. Setting minimumLines == maximumLines gives a fixed-height box.
    readonly property real textHeight: Math.min(Math.max(editor.contentHeight, lineHeight * minimumLines), lineHeight * Math.max(1, maximumLines))
    readonly property bool inputFocused: editor.activeFocus
    readonly property bool hasText: text.length > 0
    // While the input method pre-edits on-the-spot, TextEdit.text is still empty
    // and the placeholder would overlap the preedit, so count preeditText as content.
    readonly property string composingText: editor.preeditText === undefined || editor.preeditText === null ? "" : String(editor.preeditText)
    readonly property bool composing: composingText.length > 0

    signal accepted
    signal textEdited
    // Escape reaches the dialog that hosts this field; the inline composer ignores it.
    signal escapePressed
    // Emitted when the clipboard holds files or an image instead of text.
    signal filesPasted(var paths)

    function forceActiveFocus() {
        editor.forceActiveFocus();
    }
    function selectAll() {
        editor.selectAll();
    }
    function clear() {
        editor.clear();
    }

    // Keep the caret inside the viewport. TextEdit has no scrolling of its own, and
    // without this every keystroke past the last visible line would be invisible.
    function ensureCursorVisible() {
        const rect = editor.cursorRectangle;
        if (!rect || rect.height <= 0)
            return;
        const top = rect.y - 2;
        const bottom = rect.y + rect.height + 2;
        if (top < viewport.contentY)
            viewport.contentY = Math.max(0, top);
        else if (bottom > viewport.contentY + viewport.height)
            viewport.contentY = bottom - viewport.height;
    }

    // Paste entry point. Asks PasteProbe whether the clipboard holds files; those
    // become attachments via filesPasted, anything else is inserted as text.
    function pasteFromClipboard() {
        pasteProbe.probe();
    }

    // Insert text at the cursor, replacing the selection (plain-text paste path).
    function insertText(value) {
        const t = String(value || "");
        if (t === "")
            return;
        if (editor.selectedText !== "")
            editor.remove(editor.selectionStart, editor.selectionEnd);
        const pos = editor.cursorPosition;
        editor.insert(pos, t);
        editor.cursorPosition = pos + t.length;
        editor.forceActiveFocus();
    }

    width: parent ? parent.width : 0
    height: textHeight + verticalPadding * 2
    radius: cornerRadius
    color: backgroundColor
    border.width: inputFocused ? focusedBorderWidth : borderWidth
    border.color: inputFocused ? focusedBorderColor : normalBorderColor

    Behavior on border.color {
        enabled: Theme.animationsEnabled

        ColorAnimation {
            duration: Style.expressiveDurations.expressiveEffects
            easing.type: Easing.BezierSpline
            easing.bezierCurve: Style.expressiveCurves.expressiveEffects
        }
    }

    // Focus on padding clicks; declared before the viewport so text clicks stay with TextEdit.
    MouseArea {
        anchors.fill: parent
        acceptedButtons: Qt.LeftButton
        onPressed: editor.forceActiveFocus()
    }

    // interactive stays false so dragging inside the field keeps selecting text. The wheel
    // is handled below and the scrollbar drags itself, so neither needs the flick gesture.
    Flickable {
        id: viewport

        x: root.contentPadding
        y: root.verticalPadding
        width: parent.width - root.contentPadding * 2
        height: root.textHeight
        contentWidth: width
        contentHeight: editor.contentHeight
        boundsBehavior: Flickable.StopAtBounds
        flickableDirection: Flickable.VerticalFlick
        interactive: false
        clip: true

        ScrollBar.vertical: DScrollbar {
            targetFlickable: viewport
            allowed: viewport.contentHeight > viewport.height + 1
        }

        TextEdit {
            id: editor

            width: viewport.width
            // Fill the viewport when the text is short, so clicks below the last line
            // still land in the editor.
            height: Math.max(contentHeight, viewport.height)

            textFormat: TextEdit.PlainText
            wrapMode: TextEdit.Wrap
            color: Theme.surfaceText
            selectionColor: Theme.primary
            selectedTextColor: Theme.primaryText
            font.family: Theme.fontFamily
            font.pixelSize: Theme.fontSizeMedium
            renderType: Text.NativeRendering
            activeFocusOnPress: true

            onTextChanged: root.textEdited()
            onCursorRectangleChanged: root.ensureCursorVisible()

            // Wheel is explicit: TextEdit has no scrolling of its own and the Flickable is
            // not interactive, so without this a long draft could only be moved with the bar.
            WheelHandler {
                acceptedDevices: PointerDevice.Mouse | PointerDevice.TouchPad
                onWheel: event => {
                    const max = Math.max(0, viewport.contentHeight - viewport.height);
                    viewport.contentY = Math.max(0, Math.min(max, viewport.contentY - event.angleDelta.y));
                    event.accepted = true;
                }
            }

            Keys.onPressed: event => {
                const ctrl = (event.modifiers & Qt.ControlModifier) !== 0;
                const shift = (event.modifiers & Qt.ShiftModifier) !== 0;

                // Ctrl+Shift+V means "paste as plain text" and is left to TextEdit.
                if ((event.key === Qt.Key_V && ctrl && !shift) || (event.key === Qt.Key_Insert && shift && !ctrl)) {
                    event.accepted = true;
                    root.pasteFromClipboard();
                    return;
                }
                if (event.key === Qt.Key_Escape) {
                    event.accepted = true;
                    root.escapePressed();
                    return;
                }
                if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) {
                    if (event.modifiers & (Qt.ShiftModifier | Qt.ControlModifier)) {
                        event.accepted = false;
                        return;
                    }
                    event.accepted = true;
                    root.accepted();
                }
            }
        }
    }

    StyledText {
        visible: !root.hasText && !root.composing && !(root.hidePlaceholderOnFocus && root.inputFocused)
        x: root.contentPadding + 1
        y: root.verticalPadding + 1
        width: parent.width - root.contentPadding * 2
        text: root.placeholderText
        color: root.placeholderColor
        font.pixelSize: Theme.fontSizeMedium
        elide: Text.ElideRight
    }

    PasteProbe {
        id: pasteProbe

        onFilesFound: paths => root.filesPasted(paths)
        onTextFound: text => root.insertText(text)
    }
}
