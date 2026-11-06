import QtQuick
import qs.Common
import qs.Services
import qs.DCommon.Common
import "../../Common/Blockizer.js" as Blockizer

// A run of prose — paragraphs, headings, lists and quotes — in ONE TextEdit.
//
// One TextEdit per block (what this used to be) meant a selection could never leave the block it
// started in, and copying an answer meant copying it a paragraph at a time. Grouping the run fixes
// that: Qt's own selection now spans the whole thing, with no custom selection code and no extra
// C++.
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
        "h1": muted ? Theme.fontSizeMedium + 1 : Theme.fontSizeXLarge,
        "h2": muted ? Theme.fontSizeMedium : Theme.fontSizeLarge,
        "h3": muted ? Theme.fontSizeMedium : Theme.fontSizeMedium + 1,
        "font": Theme.fontFamily,
        "mono": Theme.monoFontFamily,
        "text": muted ? Theme.surfaceVariantText : Theme.surfaceText,
        "quote": Theme.surfaceVariantText,
        "codeBg": Theme.surfaceContainerHigh,
        "codeFg": Theme.surfaceText,
        // Column spacing between blocks is spacingM; inside one TextEdit that gap has to be a margin.
        "gap": Theme.spacingM,
        // A heading used to add spacingS of top padding on top of the column gap.
        "headGap": Theme.spacingS,
        "quoteIndent": Theme.spacingM
    })

    width: parent ? parent.width : 0
    readOnly: true
    selectByMouse: true
    persistentSelection: true
    textFormat: TextEdit.RichText
    wrapMode: TextEdit.Wrap

    // Assigned, never bound.
    //
    // Qt re-parses the whole rich text on every assignment even when the string is identical (measured
    // at ~3.8 ms for a 20 KB answer), and a re-parse also drops the selection. Streaming rebuilds the
    // block list on every delta, so a binding would re-assign all of this text many times a second —
    // which would both cost the layout and wipe out a selection the user is dragging across a reply
    // that is still arriving. Generating the HTML is cheap; assigning it is not.
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
