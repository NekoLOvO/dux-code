import QtQuick
import qs.Common
import qs.Services
import "../../Common/Blockizer.js" as Blockizer

// A run of prose — paragraphs, headings, lists, quotes and tables — in ONE TextEdit.
//
// Grouping the run lets Qt's own selection span it, with no custom selection code and no extra C++;
// one TextEdit per block would confine a selection to the block it started in.
//
// RichText rather than MarkdownText so every size and colour comes from Theme. Qt's Markdown importer
// scales headings 2x / 1.5x / 1.1875x off the body font, which has nothing to do with this design's
// 20 / 16 / 15.
TextEdit {
    id: root

    property var blk: ({})
    // Inside thinking blocks: secondary color and one size smaller, so the answer keeps focus.
    property bool muted: false

    // What Blockizer.toHtml needs to build the paragraph styles. Read from Theme here rather than in
    // the .js, which has no access to QML singletons.
    readonly property var proseStyle: ({
        "base": muted ? Theme.fontSizeSmall : Math.round(Theme.fontSizeMedium * 1.02),
        // H1..H6, in order. The top three are the design's 20 / 16 / 15; below that the scale has
        // only 14 left, so the last two are told apart by colour (see TextBlock's colour rule)
        // rather than by a size difference nobody can see.
        "sizes": muted
            ? [Theme.fontSizeMedium + 1, Theme.fontSizeMedium, Theme.fontSizeMedium, Theme.fontSizeSmall,
               Theme.fontSizeSmall, Theme.fontSizeSmall]
            : [Theme.fontSizeXLarge, Math.round(Theme.fontSizeLarge * 1.06), Theme.fontSizeLarge,
               Theme.fontSizeMedium + 1, Theme.fontSizeMedium, Theme.fontSizeMedium],
        // Code nested in a quote or a list item renders as monospace text here, not as a card.
        "small": Theme.fontSizeSmall,
        "font": Theme.fontFamily,
        "mono": Theme.monoFontFamily,
        "text": muted ? Theme.surfaceVariantText : Theme.surfaceText,
        "quote": Theme.surfaceVariantText,
        "accent": Theme.primary,
        "codeBg": Theme.surfaceContainerHigh,
        "codeFg": Theme.surfaceText,
        // Column spacing between blocks is spacingM; inside one TextEdit that gap has to be a margin.
        "gap": Theme.spacingM,
        // A heading gets spacingS of top padding on top of the column gap.
        "headGap": Theme.spacingS,
        "quoteIndent": Theme.spacingM,
        "quotePad": Theme.spacingS,
        // A quote is a tinted block rather than a bar down its left side: Qt's rich text paints no
        // border on a block element, and a table cell's background covers only its first line.
        // Nested quotes step one container-tone darker so the depth is visible.
        "quoteBg": Theme.surfaceContainerHigh,
        "quoteBgDeep": Theme.surfaceContainerHighest,
        "markBg": Theme.tertiaryContainer,
        "listIndent": Theme.spacingL,
        // Table chrome. The grid is the same hairline a code card's border uses, and cellpadding is
        // the only padding Qt's table layout offers (a cell's background is inset by it, so the header
        // row is drawn by weight instead).
        "tableLine": Theme.outlineVariant,
        "cellPad": Theme.spacingS
    })

    width: parent ? parent.width : 0
    readOnly: true
    selectByMouse: true
    persistentSelection: true
    textFormat: TextEdit.RichText
    wrapMode: TextEdit.Wrap

    // Assigned, never bound.
    //
    // Qt re-parses the whole rich text on every assignment even when the string is identical, and a
    // re-parse drops the selection. Streaming rebuilds the block list on every delta, so a binding
    // would re-assign all of this text many times a second: costly, and it would wipe a selection the
    // user is dragging across a reply that is still arriving. Generating the HTML is cheap; assigning
    // it is not.
    text: ""
    property string _shown: ""

    function _refresh() {
        const html = Blockizer.toHtml(blk, proseStyle);
        if (html === _shown)
            return;
        _shown = html;
        text = html;
    }

    // Selecting here clears whatever was selected elsewhere — another paragraph, a code card, a
    // reply further up. Qt only does that within one TextEdit; see Services/Selection.qml.
    onSelectedTextChanged: if (selectedText !== "") Selection.claim(root)
    // A click is a new selection too, of nothing, and the old one should not survive it. Focus is
    // the signal for that: the cursor also moves when the text is rewritten, and a streaming reply
    // would take the selection away from whatever the user was reading further up.
    onActiveFocusChanged: if (activeFocus) Selection.claim(root)
    Component.onDestruction: Selection.release(root)

    onBlkChanged: _refresh()
    onProseStyleChanged: _refresh()
    Component.onCompleted: _refresh()

    leftPadding: 0
    rightPadding: 0
    topPadding: 0
    bottomPadding: 0

    color: muted ? Theme.surfaceVariantText : Theme.surfaceText
    selectionColor: Theme.primary
    selectedTextColor: Theme.primaryText
    renderType: Text.NativeRendering
    font.family: Theme.fontFamily
    font.pixelSize: proseStyle.base

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
    // RichText paints linked text, but TextEdit has no linkActivated (that is Text).
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
