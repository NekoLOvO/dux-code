import QtQuick
import qs.Common
import qs.Services
import qs.DCommon.Widgets

// Built on DCommon's DListView: scrollbar, wheel, touchpad and edge fade are handled there,
// and the scrollbar auto-hides like in the settings page and file browser.
DListView {
    id: list

    model: Chat.messages
    clip: true
    showScrollBar: true
    spacing: Theme.spacingXL
    boundsBehavior: Flickable.StopAtBounds
    pixelAligned: true

    // --- keep contentHeight exact ---
    // Message heights vary (longer markdown grows), and ListView can only estimate unbuilt
    // delegates from the average of the built ones. Every new delegate shifts contentHeight,
    // and dragging maps mouse -> position -> contentY, so the same position lands elsewhere:
    // with contentHeight jumping between 88k/120k/180k, contentY fell from 69767 to 58400
    // while dragging down.
    // Keeping every delegate alive pins contentHeight at 88140 and contentY advances
    // linearly. dfiles feels smooth because its rows have a fixed height, not because of the
    // scrollbar. Cost: delegates are never recycled; past exactItemLimit this falls back to
    // virtualization and the estimate error returns.
    property int exactItemLimit: 300
    cacheBuffer: count <= exactItemLimit ? 1000000 : Math.max(0, height * 2)

    // --- follow the bottom ---
    // Decided from the scroll position, not from events (onMovementStarted does not fire for
    // the wheel).
    property bool follow: true
    onContentYChanged: follow = atYEnd

    readonly property bool atTop: contentY <= originY + 1

    // Content grows for two reasons (a new message, a delegate re-measured). Both re-check
    // "should follow" at run time: the queued call may run after the user hit back-to-top,
    // and using the captured value would make that tap look ignored.
    // While rows are pouring in there is nothing to look at yet, and chasing the bottom per row means a
    // layout pass and a fresh delegate for every one of them — the whole cost of a long session. The
    // scroll happens once, when the load says it is done.
    onCountChanged: if (follow && !Chat.loading) Qt.callLater(list.stickToEnd)
    onContentHeightChanged: if (follow && !Chat.loading) Qt.callLater(list.stickToEnd)

    // The load finished with no per-row change left to follow, so it says so. Twice: the first jump
    // reveals rows that were never measured, and their real heights push the bottom further down.
    Connections {
        target: Chat

        function onRowsLoaded() {
            list.follow = true;
            list.positionViewAtEnd();
            Qt.callLater(function () {
                list.positionViewAtEnd();
                list.follow = list.atYEnd;
            });
        }
    }

    function stickToEnd() {
        if (follow)
            positionViewAtEnd();
    }

    // --- a compaction folded the prefix away under us ---
    //
    // The fold is one `messages.remove(0, k)` (see Chat._foldAboveSeam) — not a reload. The model
    // shrinks under the list, which keeps its contentY: that number was measured against a transcript
    // thousands of pixels longer, and the scrollbar is drawn from contentY/contentHeight, so both end
    // up describing a document that no longer exists.
    //
    // Before/after rather than one signal, because the number that matters is how much the content
    // shrank, and only the view can measure that.
    property real _preFoldHeight: -1
    property real _preFoldY: 0

    function foldStarting() {
        _preFoldHeight = contentHeight;
        _preFoldY = contentY;
    }

    function folded() {
        // Two passes. The first runs before the new delegates are measured, so contentHeight is
        // still the old estimate; the second sees the real one.
        Qt.callLater(list.settleAfterFold);
        Qt.callLater(function () {
            Qt.callLater(list.settleAfterFold);
        });
    }

    function settleAfterFold() {
        if (_preFoldHeight < 0)
            return;
        if (follow) {
            positionViewAtEnd();
            return;
        }
        // Not following: the rows above the viewport went away, so everything below slid up by
        // exactly what the content lost. Reading position preserved, clamped to what is left.
        const lost = _preFoldHeight - contentHeight;
        if (lost > 0)
            contentY = Math.max(originY, Math.min(_preFoldY - lost, contentHeight - height));
    }

    // Back to top: turn follow off explicitly before jumping. positionViewAtBeginning() alone
    // is not enough — a pending stickToEnd or a height change can pull it back, so it is
    // repeated after the jump.
    function scrollToTop() {
        follow = false;
        positionViewAtBeginning();
        Qt.callLater(list.verifyTop);
    }

    function verifyTop() {
        if (follow)
            return;
        contentY = originY;
    }

    signal editRequested(int index, string text)
    signal regenerateRequested(int index)
    signal branchRequested(int index)
    signal continueRequested(int index)

    // --- search jump ---
    // Scroll to the matched row and flash a frame around it. Follow must be off first, or
    // stickToEnd drags the view back to the bottom.
    property int highlightedIndex: -1

    readonly property Timer highlightTimer: Timer {
        interval: 1600
        onTriggered: list.highlightedIndex = -1
    }

    Connections {
        target: Chat

        function onSeamFoldStarting() {
            list.foldStarting();
        }

        function onSeamFolded() {
            list.folded();
        }

        function onSeamRevealRequested() {
            // scrollToTop turns follow off first; a pending stickToEnd would pull the view back down.
            list.scrollToTop();
        }

        function onJumpRequested(index) {
            if (index < 0 || index >= list.count)
                return;
            list.follow = false;
            list.positionViewAtIndex(index, ListView.Center);
            list.highlightedIndex = index;
            list.highlightTimer.restart();
        }
    }

    delegate: MessageDelegate {
        isUser: model.isUser
        status: model.status
        rawText: model.rawText
        attachments: model.attachments === undefined ? "" : model.attachments
        kind: model.kind === undefined ? "text" : model.kind
        toolName: model.toolName === undefined ? "" : model.toolName
        toolCallId: model.toolCallId === undefined ? "" : model.toolCallId
        elapsedMs: model.elapsedMs === undefined ? 0 : model.elapsedMs
        truncated: model.truncated === undefined ? false : model.truncated
        usageOut: model.usageOut === undefined ? 0 : model.usageOut
        reasoning: model.reasoning === undefined ? "" : model.reasoning
        toolsJson: model.toolsJson === undefined ? "[]" : model.toolsJson
        highlighted: index === list.highlightedIndex
        // The compaction seam sits on a row of the transcript rather than in the model, so row
        // indices, search jumps and edit/regenerate addressing do not move when a summary appears.
        showCompaction: index === Chat.compactionRow
        compactionCount: Chat.compaction ? Chat.compaction.count : 0
        compactionSummary: Chat.compaction ? Chat.compaction.summary : ""

        onEditRequested: (i, text) => list.editRequested(i, text)
        onRegenerateRequested: i => list.regenerateRequested(i)
        onBranchRequested: i => list.branchRequested(i)
        onContinueRequested: i => list.continueRequested(i)
    }
}
