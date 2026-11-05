import QtQuick
import qs.Common
import qs.DCommon.Common
import "../../Common/Blockizer.js" as Blockizer

// Paragraphs, headings, lists and quotes go through Qt's MarkdownText (bold, italic, inline code, links).
TextEdit {
    id: root

    property var blk: ({})
    // Inside thinking blocks: secondary color and one size smaller, so the answer keeps focus.
    property bool muted: false

    readonly property string kind: (blk && blk.type) ? blk.type : "p"
    readonly property bool isHeading: kind === "h1" || kind === "h2" || kind === "h3"

    width: parent ? parent.width : 0
    readOnly: true
    selectByMouse: true
    persistentSelection: true
    textFormat: TextEdit.MarkdownText
    wrapMode: TextEdit.Wrap
    text: Blockizer.toMarkdown(blk)

    leftPadding: 0
    rightPadding: 0
    topPadding: isHeading ? Theme.spacingS : 0
    bottomPadding: 0

    color: muted || kind === "quote" ? Theme.surfaceVariantText : Theme.surfaceText
    selectionColor: Theme.primary
    selectedTextColor: Theme.primaryText
    renderType: Text.NativeRendering
    font.family: Theme.fontFamily
    font.pixelSize: {
        if (muted)
            return isHeading ? Theme.fontSizeMedium + (kind === "h1" ? 1 : 0) : Theme.fontSizeSmall;
        return kind === "h1" ? Theme.fontSizeXLarge
               : kind === "h2" ? Theme.fontSizeLarge
               : kind === "h3" ? Theme.fontSizeMedium + 1
               : Theme.fontSizeMedium * 1.02;
    }
    font.weight: isHeading ? Font.DemiBold : Theme.fontWeight

    // TextEdit.linkAt uses content coordinates, which exclude padding. Padding is 0 today; do not bake that assumption in.
    function linkUnder(x, y) {
        if (x < 0 || y < 0 || x > width || y > height)
            return "";
        return root.linkAt(x - root.leftPadding, y - root.topPadding) || "";
    }

    function openExternal(url) {
        if (!url)
            return;
        // openUrlExternally hands off to xdg-open. Only these three schemes: the answer comes
        // from the model (often from a web page) and must not reach file: or any other
        // registered handler.
        if (/^(https?:\/\/|mailto:)/i.test(url))
            Qt.openUrlExternally(url);
    }

    // --- clickable links ---
    // MarkdownText paints [text](url) blue, but TextEdit has no linkActivated (that is Text).
    // A thin layer sits on top: press a link to open it, press elsewhere to let the event
    // through, so selection and double-click still work.
    MouseArea {
        anchors.fill: parent
        hoverEnabled: true
        acceptedButtons: Qt.LeftButton
        // Pointing hand over links, I-beam elsewhere (TextEdit's own cursor).
        cursorShape: root.linkUnder(mouseX, mouseY) !== "" ? Qt.PointingHandCursor : Qt.IBeamCursor

        onPressed: mouse => {
            const url = root.linkUnder(mouse.x, mouse.y);
            if (url === "") {
                // Not accepted, so the event falls through to TextEdit and selection still works.
                mouse.accepted = false;
                return;
            }
            mouse.accepted = true;
            root.openExternal(url);
        }
    }
}
