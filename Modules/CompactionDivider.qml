import QtQuick
import qs.Common
import qs.Services
import qs.DCommon.Common
import qs.DCommon.Widgets
import qs.Modules.Blocks
import "../Common/Blockizer.js" as Blockizer

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
    property bool open: false

    // The folded prefix, as transcript rows (Chat.archived). Built lazily, chunk by chunk.
    property var archive: null
    // A message id to reveal in the archive (a search landed there). Cleared once shown.
    property string revealId: ""

    // One wording for the pill and for assistive technology; a second copy would drift.
    readonly property string label: count === 1
        ? I18n.tr("Summary of 1 earlier message", "compaction seam in the transcript: a summary replaced exactly one message")
        : I18n.tr("Summary of %1 earlier messages", "compaction seam in the transcript: a summary replaced this many messages").arg(count)

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
                archiveList.positionViewAtIndex(i, ListView.Center)
                break
            }
        }
        Chat.archiveJump = ""
    }

    onRevealIdChanged: if (revealId !== "") {
        root.open = true
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
                    text: root.open ? "expand_less" : "expand_more"
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
                onClicked: root.open = !root.open
            }
        }

        // ---- the card: summary, or what the summary replaced ----
        Rectangle {
            id: card

            width: parent.width
            visible: root.open
            height: visible ? content.implicitHeight + Theme.spacingM * 2 : 0
            radius: Theme.cornerRadiusM
            color: Theme.surfaceContainer
            border.width: Theme.outlineWidth
            border.color: Theme.outlineVariant
            clip: true

            Column {
                id: content

                x: Theme.spacingM
                y: Theme.spacingM
                width: parent.width - Theme.spacingM * 2
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
                ListView {
                    id: archiveList

                    width: parent.width
                    visible: root.tab === "archive"
                    height: visible ? Math.min(contentHeight, root.archiveMaxHeight) : 0
                    clip: true
                    spacing: Theme.spacingM
                    boundsBehavior: Flickable.StopAtBounds
                    model: root.archive

                    delegate: Item {
                        // Roles rather than modelData: a ListModel hands its fields out as named
                        // properties, and modelData only exists for a plain JS array.
                        required property int index
                        required property bool isUser
                        required property string kind
                        required property string rawText
                        required property string toolsJson

                        width: archiveList.width
                        height: entry.implicitHeight

                        Column {
                            id: entry

                            width: parent.width
                            spacing: Theme.spacingXS

                            StyledText {
                                text: entry.kind === "tool"
                                    ? I18n.tr("%1 tool calls", "compaction archive: how many tool calls sat in one archived row").arg((JSON.parse(entry.toolsJson || "[]") || []).length)
                                    : (entry.isUser
                                        ? I18n.tr("You", "compaction archive: this archived message is from the user")
                                        : I18n.tr("Assistant", "compaction archive: this archived message is from the model"))
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceVariantText
                            }

                            TextBlock {
                                width: parent.width
                                visible: entry.kind !== "tool"
                                height: visible ? implicitHeight : 0
                                muted: true
                                blk: {
                                    // The archived row is already prose-shaped; the blockizer turns it
                                    // into the same HTML the transcript uses.
                                    return { "type": "prose", "items": Blockizer.parse(entry.rawText || "") }
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
