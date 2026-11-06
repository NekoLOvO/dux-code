import QtQuick
import qs.Common
import qs.Services
import qs.DCommon.Common
import qs.DCommon.Widgets
import qs.Modules.Blocks
import "../Common/Blockizer.js" as Blockizer
import "../Common/TextFmt.js" as TextFmt

// The line where a compaction takes over.
//
// Everything above it is no longer sent to the model; a summary stands in for it. A number in a
// chip could only say "some messages are gone", which is both alarming and unactionable — the
// useful thing is the boundary itself, and the text that replaced the part above it.
//
// Collapsed by default: it is a seam in the transcript, not content. Expanded it has two tabs — the
// summary the model works from, and the original messages it replaced.
//
// The original messages are folded out of the transcript entirely (Chat folds them at load), which is
// what keeps a long, compacted session cheap to open: they are most of the rows, they are what the
// model no longer reads, and building them costs the layout pass this feature exists to avoid. They are
// built here, in chunks, only when someone opens that tab — and they are for reading, not acting on:
// there is no edit or regenerate inside the archive, because rewriting history the model only sees as a
// summary is a different decision than editing what it just said.
Item {
    id: root

    // How many messages the summary replaced (the daemon's count).
    property int count: 0
    property string summary: ""

    // The folded prefix, as transcript rows (Chat.archived). Built lazily, chunk by chunk.
    property var archive: null
    // A message id to reveal in the archive (a search landed there). Cleared once shown.
    property string revealId: ""

    // One wording for the pill and for assistive technology; a second copy would drift. It is shared
    // with the chip above the composer, which stands for the same summary from a different place.
    readonly property string label: Chat.compactionLabel(count)

    // Which tab the card is showing. The summary is the default: it is short, it is what the model
    // actually reads, and the archive can be hundreds of rows.
    property string tab: "summary"
    // The summary as the same block HTML the transcript uses, so a summary containing a list or a table
    // renders instead of showing its own Markdown.
    readonly property var summaryBlock: {
        const blocks = Blockizer.parse(String(root.summary))
        return { "type": "prose", "items": blocks }
    }
    readonly property bool empty: root.archive === null || root.archive.count === 0
    // One card cannot be allowed to fill the window; past this it scrolls.
    readonly property real archiveMaxHeight: Math.round(Theme.fontSizeMedium * 26)

    function selectTab(key) {
        root.tab = key
        if (key === "archive") {
            // Built on first open, in chunks: a long prefix is hundreds of rows.
            Chat.buildArchive()
            Qt.callLater(root.revealPending)
        }
    }

    // A search landed inside the folded prefix: open the archive and scroll to that row.
    function revealPending() {
        if (root.revealId === "" || !root.archive)
            return
        for (let i = 0; i < root.archive.count; i++) {
            const ids = String(root.archive.get(i).msgIds || "")
            if (ids !== "" && ("," + ids + ",").indexOf("," + root.revealId + ",") >= 0) {
                archiveList.revealIndex(i)
                break
            }
        }
        Chat.archiveJump = ""
    }

    onRevealIdChanged: if (revealId !== "") {
        Chat.seamOpen = true
        root.selectTab("archive")
    }

    implicitHeight: column.implicitHeight
    height: implicitHeight

    Column {
        id: column

        width: parent.width
        spacing: Theme.spacingS

        // ---- the seam: rule, pill, rule ----
        Item {
            width: parent.width
            height: Theme.buttonHeightXS

            Rectangle {
                anchors.left: parent.left
                anchors.right: pill.left
                anchors.rightMargin: Theme.spacingS
                anchors.verticalCenter: parent.verticalCenter
                height: Theme.dividerWidth
                color: Theme.outlineVariant
            }

            Rectangle {
                anchors.left: pill.right
                anchors.leftMargin: Theme.spacingS
                anchors.right: parent.right
                anchors.verticalCenter: parent.verticalCenter
                height: Theme.dividerWidth
                color: Theme.outlineVariant
            }

            Row {
                id: pill

                anchors.centerIn: parent
                spacing: Theme.spacingXS

                StyledText {
                    anchors.verticalCenter: parent.verticalCenter
                    text: "compress"
                    font.family: Fonts.icons
                    font.pixelSize: Theme.iconSizeSmall
                    color: Theme.surfaceVariantText
                }

                StyledText {
                    anchors.verticalCenter: parent.verticalCenter
                    text: root.label
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                }

                StyledText {
                    anchors.verticalCenter: parent.verticalCenter
                    text: Chat.seamOpen ? "expand_less" : "expand_more"
                    font.family: Fonts.icons
                    font.pixelSize: Theme.iconSizeSmall
                    color: Theme.surfaceVariantText
                }
            }

            // A thin hit area over the whole seam, so the rule is part of the button rather than
            // decoration next to it.
            MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                Accessible.role: Accessible.Button
                Accessible.name: root.label
                onClicked: Chat.seamOpen = !Chat.seamOpen
            }
        }

        // ---- the card: summary, or what the summary replaced ----
        // The library's DCard, so the surface, radius and layer outline are the transcript's rather
        // than a second opinion. Hand-drawn, this card was the only one in the window with a visible
        // 1 px border: DCard takes its outline from Style.layerOutlineWidth, which is 0 here because
        // SettingsData.blurLayerOutlineOpacity is.
        DCard {
            id: card

            width: parent.width
            visible: Chat.seamOpen
            height: visible ? content.implicitHeight + pad * 2 : 0
            pad: Theme.spacingM
            clipContent: true

            Column {
                id: content

                width: parent.width
                spacing: Theme.spacingS

                // ---- tabs ----
                Row {
                    spacing: Theme.spacingS

                    Repeater {
                        model: [
                            { "key": "summary", "label": I18n.tr("Summary", "compaction card tab: the text the model now works from") },
                            { "key": "archive", "label": I18n.tr("Original conversation", "compaction card tab: the messages the summary replaced") }
                        ]

                        delegate: Rectangle {
                            required property var modelData
                            width: tabLabel.implicitWidth + Theme.spacingM * 2
                            height: tabLabel.implicitHeight + Theme.spacingXS * 2
                            radius: height / 2
                            color: root.tab === modelData.key ? Theme.secondaryContainer : "transparent"
                            border.width: root.tab === modelData.key ? 0 : Theme.outlineWidth
                            border.color: Theme.outlineVariant

                            StyledText {
                                id: tabLabel
                                anchors.centerIn: parent
                                text: modelData.label
                                font.pixelSize: Theme.fontSizeSmall
                                color: root.tab === modelData.key ? Theme.onSecondaryContainer : Theme.surfaceVariantText
                            }

                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                Accessible.role: Accessible.Button
                                Accessible.name: modelData.label
                                onClicked: root.selectTab(modelData.key)
                            }
                        }
                    }
                }

                // ---- summary ----
                TextBlock {
                    width: parent.width
                    visible: root.tab === "summary"
                    height: visible ? implicitHeight : 0
                    blk: root.summaryBlock
                    muted: true
                }

                // What "compacted" costs, said once, where it matters: this text is what the model
                // now works from, and the messages it stands for are no longer part of the request.
                StyledText {
                    width: parent.width
                    visible: root.tab === "summary"
                    height: visible ? implicitHeight : 0
                    text: I18n.tr("The model reads this summary instead of the messages it replaced. They are still here, under Original conversation.", "compaction card: explanation under the summary tab")
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                    wrapMode: Text.WordWrap
                }

                // ---- the original conversation ----
                //
                // Its own list with its own height: the archive is hundreds of rows, and putting them
                // in one column would build every delegate at once — the freeze, on demand. Bounded so
                // one card cannot fill the window.
                DListView {
                    id: archiveList

                    width: parent.width
                    visible: root.tab === "archive"
                    height: visible ? Math.min(contentHeight, root.archiveMaxHeight) : 0
                    clip: true
                    spacing: Theme.spacingM
                    boundsBehavior: Flickable.StopAtBounds
                    model: root.archive
                    // Hundreds of rows in a box a few hundred pixels tall, so the scrollbar and the
                    // edge fade matter: a bare ListView here gave no sign the list continued below.

                    delegate: Item {
                        id: archRow

                        // Roles rather than modelData: a ListModel hands its fields out as named
                        // properties, and modelData only exists for a plain JS array.
                        required property int index
                        required property bool isUser
                        required property string kind
                        required property string rawText
                        required property string toolsJson

                        // A tool row merges every result of one turn's parallel calls. Most of a long
                        // session's archive is these, so showing only a count would leave the tab with
                        // rows that say nothing — which is what it did.
                        readonly property var calls: {
                            if (kind !== "tool")
                                return []
                            try {
                                return JSON.parse(toolsJson || "[]") || []
                            } catch (e) {
                                return []
                            }
                        }
                        readonly property string callNames: {
                            var names = []
                            for (var i = 0; i < archRow.calls.length; i++) {
                                var n = archRow.calls[i] && archRow.calls[i].name ? archRow.calls[i].name : ""
                                if (n !== "" && names.indexOf(n) === -1)
                                    names.push(n)
                            }
                            return names.join(" · ")
                        }

                        width: archiveList.width
                        height: entry.implicitHeight

                        Column {
                            id: entry

                            width: parent.width
                            spacing: Theme.spacingXS

                            StyledText {
                                width: parent.width
                                text: {
                                    if (archRow.kind === "tool") {
                                        const base = archRow.callNames !== ""
                                            ? archRow.callNames
                                            : I18n.tr("%1 tool calls", "compaction archive: how many tool calls sat in one archived row").arg(archRow.calls.length)
                                        return archRow.calls.length > 1 ? base + " ×" + archRow.calls.length : base
                                    }
                                    return archRow.isUser
                                        ? I18n.tr("You", "compaction archive: this archived message is from the user")
                                        : I18n.tr("Assistant", "compaction archive: this archived message is from the model")
                                }
                                font.pixelSize: Theme.fontSizeSmall
                                font.weight: Theme.fontWeightMedium
                                color: Theme.surfaceText
                                isMonospace: archRow.kind === "tool"
                                elide: Text.ElideRight
                            }

                            // A message: the same prose renderer the transcript uses.
                            TextBlock {
                                width: parent.width
                                visible: archRow.kind !== "tool"
                                height: visible ? implicitHeight : 0
                                muted: true
                                blk: {
                                    return { "type": "prose", "items": Blockizer.parse(archRow.rawText || "") }
                                }
                            }

                            // A tool call: what was called, and what came back — bounded so one huge
                            // output cannot make a single archived row taller than the card.
                            Repeater {
                                model: archRow.kind === "tool" ? archRow.calls.length : 0

                                delegate: Column {
                                    required property int index

                                    width: entry.width
                                    spacing: Theme.spacingXS

                                    StyledText {
                                        width: parent.width
                                        visible: text !== ""
                                        text: TextFmt.expandTabs(String(archRow.calls[index].args || ""))
                                        font.pixelSize: Theme.fontSizeSmall
                                        color: Theme.primary
                                        isMonospace: true
                                        wrapMode: Text.WrapAnywhere
                                        maximumLineCount: 4
                                        elide: Text.ElideRight
                                    }

                                    StyledText {
                                        width: parent.width
                                        visible: text !== ""
                                        text: TextFmt.expandTabs(String(archRow.calls[index].output || "").trim())
                                        font.pixelSize: Theme.fontSizeSmall
                                        color: Theme.surfaceVariantText
                                        isMonospace: true
                                        wrapMode: Text.WrapAnywhere
                                        maximumLineCount: 12
                                        elide: Text.ElideRight
                                    }
                                }
                            }
                        }
                    }
                }

                StyledText {
                    width: parent.width
                    visible: root.tab === "archive" && root.empty
                    text: I18n.tr("The original messages are not in this session file any more.", "compaction archive: the cut point was found but the messages behind it are gone")
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                    wrapMode: Text.WordWrap
                }
            }
        }
    }
}
