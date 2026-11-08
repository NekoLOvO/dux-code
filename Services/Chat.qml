pragma Singleton
import QtQuick
import qs.Services
import qs.Common

// Conversation state. One rule matters: drop any delta/finished whose run_id is not
// the active run, which keeps deleted replies from reappearing.
QtObject {
    id: chat

    readonly property int stDone: 0
    readonly property int stStreaming: 1
    readonly property int stCancelled: 2

    property string activeRunId: ""
    property int liveIndex: -1

    // A question from the model is waiting, and the user put it aside to read the conversation first.
    //
    // The dialog is modal and the daemon is blocked on the answer, so "hide it" cannot mean "dismiss
    // it": the turn would hang until it times out. It means "I will answer in a moment", and this flag
    // is what keeps the dialog closed and puts the way back in the ask_user tool card.
    property bool questionHidden: false

    // The tool card's button asks for the dialog back; the dialog owns itself and listens for this,
    // the same way it already listens to Backend for new questions.
    signal questionRestoreRequested

    function restoreQuestion() {
        questionRestoreRequested();
    }

    // Opening a *new* question always surfaces it: a flag left over from the previous one would hide a
    // question the user has not seen yet. A question that stops pending — answered from the CLI, or
    // voided with the connection — clears it too, so nothing is left suppressing a dialog for a
    // question that no longer exists.
    readonly property Connections _questionLink: Connections {
        target: Backend

        function onQuestionPending() {
            chat.questionHidden = false;
        }

        function onQuestionsChanged() {
            if (chat.questionHidden && Backend.questions.length === 0)
                chat.questionHidden = false;
        }
    }

    // The first message waits here until the daemon has created a session.
    property var pendingSend: null

    // Run start time, used by the "N steps (0:42)" label.
    property double runStartedAt: 0
    property int runElapsedMs: 0

    readonly property Timer runTicker: Timer {
        interval: 500
        repeat: true
        running: chat.busy && chat.runStartedAt > 0
        onTriggered: chat.runElapsedMs = Date.now() - chat.runStartedAt
    }

    property ListModel messages: ListModel {}

    // True while a session's rows are being inserted.
    //
    // Insertion is the expensive part: each row appended to the model the view is showing costs a list
    // layout, and a measured 940-row session took 8.5 seconds of blocked main thread — long enough for
    // the compositor to decide the window has stopped responding. Two things fix that and both need to
    // know a load is in progress: the rows go in a chunk at a time (so the event loop keeps turning and
    // the window answers pings), and the view stops chasing the bottom on every single one of them.
    property bool loading: false

    // Emitted once every row is in the model. The view has had no per-row change to follow, so it is
    // told to go to the newest message and measure what it shows.
    signal rowsLoaded

    // How many rows go in per event-loop turn. Small enough that one chunk is well under a frame or
    // two of work, large enough that the loop does not dominate the cost of a long session.
    readonly property int _loadChunk: 12

    // Counts loads so an older one that is still inserting can tell it has been superseded.
    property int _loadToken: 0

    // The compaction record of the session being loaded; applied once its rows are in.
    property var _pendingCompaction: null

    // What is left to insert, kept so a send that arrives mid-load can finish the job instead of
    // landing among rows that have not been written yet.
    property var _pendingRows: []
    property int _pendingFrom: 0
    property var _pendingDone: null

    // Row of the user message the current run is about to persist. chat.send answers with
    // the daemon's messageId, which is written back here: until then the row has no id and
    // cannot be edited or regenerated from.
    property int pendingUserRow: -1

    // The daemon's id for the message shown at a row, "" when the row has none yet.
    // Rows can hold several ids (merged tool results); callers act on the first one.
    function messageIdAt(index) {
        if (index < 0 || index >= messages.count) return ""
        var ids = String(messages.get(index).msgIds || "")
        return ids.split(",")[0] || ""
    }

    // The last id of a row. Branching a merged tool row has to keep every result in it,
    // not just the first.
    function lastMessageIdAt(index) {
        if (index < 0 || index >= messages.count) return ""
        var ids = String(messages.get(index).msgIds || "").split(",")
        return ids[ids.length - 1] || ""
    }

    // A row that has no id from the daemon yet, so there is nothing to address.
    //
    // Two causes, and they need different words: the reply is still arriving (the id is minted when it
    // is written, so waiting is the answer), or the run ended without persisting anything (a failed or
    // empty turn — retrying will not help, and saying "in a moment" is a lie).
    function missingId(index) {
        var streaming = index !== undefined && index >= 0 && index < messages.count
                && messages.get(index).status === stStreaming
        if (streaming) {
            ToastService.info(I18n.tr("This reply is still arriving. Wait for it to finish, then branch.", "toast when branching from a reply that is still streaming"))
        } else {
            ToastService.info(I18n.tr("That message was never saved, so there is nothing to branch from.", "toast when a message has no id because the run persisted nothing"))
        }
        return false
    }

    // --- search jump ---
    // Search results carry store messageIds, not indices: loading merges consecutive tool
    // results into one row and drops assistant messages that only hold toolCalls, so the two
    // orderings differ. After loading, scan once to find the on-screen row.
    property string jumpTarget: ""
    // A message id a search landed on that lives inside the folded prefix. The card opens its archive
    // and scrolls to it, then clears this.
    property string archiveJump: ""
    signal jumpRequested(int index)
    // Scroll to the compaction seam. It is the first row after a fold, so this is "go to the top"
    // rather than "jump to an index": ListView centres an index jump, which for row 0 lands above the
    // content (a negative contentY and an empty band).
    signal seamRevealRequested()

    // Whether the summary card is expanded.
    //
    // It lives here, not in CompactionDivider, because the divider is a delegate: past exactItemLimit
    // the row is recycled, and a card opened from the chip above the composer would be collapsed again
    // by the time its row is built.
    property bool seamOpen: false

    // Go to the summary card and open it. Both halves are needed: the card is a seam at the top of the
    // transcript, so on a view that was following the reply it is thousands of pixels up, and a
    // collapsed seam is a hairline that nothing would recognise as the summary someone asked for.
    function openSeam() {
        seamOpen = true
        seamRevealRequested()
    }

    // One wording for the seam pill, the chip above the composer and assistive technology; a second
    // copy of this sentence would drift from the first.
    function compactionLabel(n) {
        return n === 1
            ? I18n.tr("Summary of 1 earlier message", "compaction seam in the transcript: a summary replaced exactly one message")
            : I18n.tr("Summary of %1 earlier messages", "compaction seam in the transcript: a summary replaced this many messages").arg(n)
    }

    // Around `messages.remove(0, k)` in _foldAboveSeam.
    //
    // A fold is an in-place model edit, not a reload, so nothing else tells the list the transcript
    // just got shorter: ListView keeps its contentY, and the rows it held are gone. Before lets the
    // view remember where it was; after lets it put itself back.
    signal seamFoldStarting()
    signal seamFolded()

    function jumpTo(sessionId, messageId) {
        jumpTarget = messageId || "";
        if (sessionId !== "" && sessionId !== Sessions.activeId) {
            // Switching sessions triggers loadSession, which locates the jump itself.
            Sessions.select(sessionId);
        } else {
            locateJump();
        }
    }

    function locateJump() {
        if (jumpTarget === "")
            return;
        const want = jumpTarget;
        jumpTarget = "";
        // A previous jump may have left the card pointed at a row; this one decides afresh.
        archiveJump = "";
        for (let i = 0; i < messages.count; i++) {
            const ids = messages.get(i).msgIds || "";
            // Match with surrounding commas so ids that are substrings of each other do not
            // collide; one tool row can hold several ids.
            if (ids !== "" && ("," + ids + ",").indexOf("," + want + ",") >= 0) {
                jumpRequested(i);
                return;
            }
        }
        // Not in the transcript: the compacted prefix is folded away. Everything the search covers is
        // still here, so open the archive and point at the row.
        if (_archivedMessages.length > 0) {
            archiveJump = want;
            return;
        }
        // Nothing on screen holds it any more (edited or truncated after the search ran).
        ToastService.info(I18n.tr("That message is no longer in this conversation.", "toast when a search hit cannot be found in the transcript"));
    }

    readonly property bool busy: activeRunId !== ""

    // True from the moment the daemon says it is folding the earlier history until the fold is
    // reported, text starts arriving, or the turn ends. Folding is several model calls and the turn
    // sends nothing in the meantime, so without this the status line claims to be working on an
    // answer nobody has asked for yet.
    property bool compacting: false

    // Everything this session has spent, in tokens, as the daemon counts it: the sum of every
    // request's API-reported usage — all tool steps of every turn, plus the summary and title
    // requests. It is absolute and only grows; compaction shrinks what is *sent* from then on, not
    // what was already billed.
    //
    // The number is assigned from daemon events, never accumulated here: the same total arrives on
    // every step and again on run.finished.
    property int sessionTokens: 0

    function send(text, attachments) {
        var t = String(text === undefined || text === null ? "" : text).trim()
        var atts = []
        if (attachments) {
            for (let i = 0; i < attachments.length; i++) {
                const p = String(attachments[i] || "")
                if (p !== "") atts.push(p)
            }
        }
        if (t.length === 0 && atts.length === 0) return
        // A session still being inserted must be complete first, or this message lands in the middle of
        // it and the rows after it arrive underneath.
        if (loading) _finishRows()
        // Reached only by callers that mean "interrupt and redirect": the CLI (which talks to the daemon
        // directly, not through here) and the IPC send target. The composer refuses to send while a turn
        // runs — Enter does nothing and the toolbar button is the stop button — so a stray keypress can
        // never end a reply someone is reading.
        if (busy) cancel()

        // Without a session, create one first; the daemon has no implicit current session.
        if (Sessions.activeId === "") {
            pendingSend = { "text": t, "attachments": atts }
            Sessions.create()
            return
        }

        pendingUserRow = messages.count
        messages.append({
            msgIds: "",
            isUser: true,
            kind: "text",
            toolName: "",
            toolCallId: "",
            status: stDone,
            rawText: t,
            runId: "",
            elapsedMs: 0,
            attachments: atts.join("\n"),
            usageIn: 0,
            usageOut: 0,
            truncated: false,
            reasoning: "",
            toolsJson: "[]"
        })

        var rid = "run-" + Date.now()
        activeRunId = rid
        runStartedAt = Date.now()
        runElapsedMs = 0
        messages.append({
            msgIds: "",
            isUser: false,
            kind: "text",
            toolName: "",
            toolCallId: "",
            status: stStreaming,
            rawText: "",
            runId: rid,
            elapsedMs: 0,
            attachments: "",
            usageIn: 0,
            usageOut: 0,
            truncated: false,
            reasoning: "",
            toolsJson: "[]"
        })
        liveIndex = messages.count - 1

        Agent.start(rid, t, atts)
    }

    function cancel() {
        if (!busy) return
        Agent.cancel()
    }

    // The one summary standing in for earlier history: { upToId, summary, count }, or null.
    //
    // The transcript keeps every message — only what is sent is replaced — so this is shown as a seam
    // in the conversation, not by hiding rows. One summary is in play at a time: a later compaction
    // recomputes the cut against the whole history and overwrites the record, so count is "how many
    // messages the summary currently stands in for", not a sum of every compaction.
    property var compaction: null
    // The row that seam is drawn above, -1 when there is none.
    property int compactionRow: -1

    // Adopt a compaction record (from the daemon's event or from a loaded session) and find which row
    // the seam belongs above.
    function _setCompaction(c) {
        // A record with no cut point, or with nothing dropped, describes no boundary.
        compaction = (c && c.upToId && (c.count || 0) > 0)
            ? { "upToId": c.upToId, "summary": c.summary || "", "count": c.count }
            : null
        _locateCompaction()
        _foldAboveSeam()
    }

    // A row copied into a plain object.
    //
    // `messages.get(i)` hands back a model element, not a value: appending it to another ListModel
    // copies nothing, and the archive renders rows with no text at all. Only the roles the archive
    // delegate reads are carried over.
    function _rowCopy(r) {
        return {
            "msgIds": r.msgIds || "",
            "isUser": r.isUser === true,
            "kind": r.kind || "text",
            "rawText": r.rawText || "",
            "toolsJson": r.toolsJson || "[]"
        }
    }

    // Rows folded into the archive, waiting for the archive to be built before they can join it.
    property var _queuedFolded: []

    // Move every row above the seam into the archive.
    //
    // At load time the rows are already folded (loadSession builds from the cut), so this does
    // nothing. It matters mid-session: a compaction fires at the start of a turn, and the rows it just
    // covered are still in the model. One remove() call rather than a loop — removing a long prefix row
    // by row is a layout pass each, which is the freeze this feature exists to avoid.
    function _foldAboveSeam() {
        if (!compaction || compactionRow <= 0)
            return
        var k = compactionRow
        if (_archivedBuilt) {
            for (let i = 0; i < k; i++)
                archived.append(_rowCopy(messages.get(i)))
        } else {
            // buildArchive has not run, so its rows must land first or the order would be wrong.
            for (let j = 0; j < k; j++)
                _queuedFolded.push(_rowCopy(messages.get(j)))
        }
        // Indices that point into the model move up with the rows.
        if (liveIndex >= 0)
            liveIndex -= k
        if (pendingUserRow >= 0)
            pendingUserRow -= k
        seamFoldStarting()
        messages.remove(0, k)
        compactionRow = 0
        seamFolded()
        // Say so. The seam is the first row now, which puts the card at the very top of the
        // transcript — thousands of pixels above a view that is following the reply, so a compaction
        // that fired during a turn changed nothing on screen. Nothing else in the window indicates it.
        // compaction.count, not k: k is rows, and a row merges several tool results, so the two numbers
        // differ (371 rows were 635 messages here) and the card shows the message count.
        ToastService.show(
            I18n.tr("Folded %1 earlier messages into a summary.", "toast after compaction folded the earlier part of the conversation into the summary card").arg(compaction.count),
            {
                "actionLabel": I18n.tr("Show", "toast action: scroll to the compaction summary card"),
                "action": function () {
                    chat.openSeam();
                }
            });
    }

    // The cut point is a message id; the row is found by id because rows merge and drop messages.
    function _locateCompaction() {
        let row = -1
        if (compaction) {
            for (let i = 0; i < messages.count; i++) {
                if (String(messages.get(i).msgIds || "").split(",").indexOf(compaction.upToId) !== -1) {
                    row = i
                    break
                }
            }
        }
        // Not found anywhere in the rows, and the prefix is folded: then the seam is the first row
        // whichever id it names. Without this the card could disappear — and the card is the only way
        // back to the archive. It must stay a *fallback*, though: checked first it would pin every
        // later compaction to row 0, so a second one would fold nothing and leave its own boundary
        // drawn in the wrong place.
        // archived.count is 0 until the card's tab is opened, so the loaded-but-not-yet-built prefix
        // counts too, or the fallback would not apply exactly when it is needed (right after a load).
        if (compaction && row < 0 && (archived.count > 0 || _archivedMessages.length > 0))
            row = 0
        compactionRow = row
    }

    function _compacting(sessionId, count) {
        if (sessionId !== "" && sessionId !== Sessions.activeId)
            return
        compacting = true
        ToastService.show(count > 0
            ? I18n.tr("Compressing %1 earlier messages into a summary…", "toast while the older part of the conversation is being folded, %1 is a message count").arg(count)
            : I18n.tr("Compressing the earlier conversation…", "toast while the older part of the conversation is being folded"))
    }

    function _compacted(sessionId, data) {
        if (sessionId !== "" && sessionId !== Sessions.activeId)
            return
        compacting = false
        _setCompaction(data)
    }

    // Switch session: reload persisted messages, and drop the streaming run (it belongs to the old one).
    function loadSession(id, done) {
        // Every turn belongs to the daemon, including one this window started: switching sessions
        // detaches from it instead of cancelling it. Cancelling here is what made "look at another
        // conversation while the reply is still coming" throw the reply away — the user was reading a
        // half-written answer and the switch stopped it.
        //
        // Detached, the daemon keeps writing (and persists the rest), and coming back adopts the turn:
        // session.get says busy and reports what the running step has already written. Stopping a turn
        // is the stop button's job, not the sidebar's.
        if (busy)
            Agent.detach()
        messages.clear()
        loading = true
        _loadToken++
        // Set by the reply to this load. Cleared here so a failed or superseded load cannot leave a
        // prefix behind for the next adopt to seed a bubble with.
        _pendingRunningText = ""
        _pendingRunningReasoning = ""
        _pendingStartedAt = 0
        pendingUserRow = -1
        activeRunId = ""
        liveIndex = -1
        compacting = false
        compaction = null
        compactionRow = -1
        seamOpen = false
        sessionTokens = 0
        if (!id) {
            loading = false
            if (done) done()
            // Nothing to scroll to without a session; drop the pending target so a later
            // session does not jump to an unrelated position.
            jumpTarget = ""
            return
        }

        // Two loads of the same session — a reload on connect racing a click — both pass the id
        // check below, and the second one appends its rows on top of the first's: the transcript
        // appears twice. The token tells them apart; only the newest load may install its rows.
        const token = _loadToken;
        Backend.sendRequest("session.get", { "id": id }, function (res, err) {
            if (token !== _loadToken)
                return
            if (err || !res || !res.session) {
                // Nothing arrived, so the rows on screen belong to a session that is no longer selected.
                // Show nothing rather than another conversation under this title.
                loading = false
                if (id === Sessions.activeId)
                    messages.clear()
                return
            }
            if (id !== Sessions.activeId) { // Switched again mid-load.
                loading = false
                return
            }
            // Set after the rows are built: the seam needs a row to sit above.
            var savedCompaction = res.session.compaction || null
            // session.get reports whether a turn is running; _finishRows acts on it once the rows are in.
            // The step in flight is not on disk yet, so whatever it has written travels with the answer —
            // without it, returning to a session mid-reply shows the user message and an empty bubble
            // until the turn ends.
            _pendingBusy = res.busy === true
            _pendingRunningText = res.runningText || ""
            _pendingRunningReasoning = res.runningReasoning || ""
            _pendingStartedAt = res.startedAt || 0
            // The daemon keeps the running total (and recovers it for files written before it
            // existed), so opening a session shows its spend without replaying a single turn.
            const tu = res.session.totalUsage
            sessionTokens = tu ? (tu.input || 0) + (tu.output || 0) : 0
            var list = res.session.messages || []
            var callArgs = _callArgsOf(list)
            // Fold the part the summary stands in for: those messages are what the model no longer
            // reads, and on a long session they are most of the rows. They stay reachable through the
            // card's second tab, which builds them only when someone asks.
            var cut = _cutIndex(list, savedCompaction)
            _archivedMessages = cut > 0 ? list.slice(0, cut) : []
            _archivedCallArgs = callArgs
            _archivedBuilt = false
            _queuedFolded = []
            archived.clear()
            var rows = []
            if (cut > 0)
                rows = _buildRows(list, cut, list.length, callArgs)
            else
                rows = _buildRows(list, 0, list.length, callArgs)
            // The rows are built; putting them into the model is the part that has to be spread out.
            _pendingCompaction = savedCompaction
            _insertRows(rows, 0, _loadToken, done)
        })
    }

    // --- fold: the compacted prefix, and the archive behind the card ---

    // The store messages the summary stands in for, kept so the card's second tab can build them —
    // only when someone opens it. Empty when no compaction covers this session.
    property var _archivedMessages: []
    property var _archivedCallArgs: ({})
    property bool _archivedBuilt: false

    // The archive as the card renders it. A ListModel rather than a JS array because it arrives in
    // chunks (see buildArchive) and a plain array would make every chunk a full re-render.
    property ListModel archived: ListModel {}

    readonly property int archiveCount: _archivedMessages.length

    // Index in `list` of the first message still sent verbatim, or -1 when the record names a message
    // that is not there (an edited transcript, an older file).
    function _cutIndex(list, record) {
        if (!record || !record.upToId)
            return -1
        for (let i = 0; i < list.length; i++) {
            if (list[i] && list[i].id === record.upToId)
                return i
        }
        return -1
    }

    // Tool arguments for older files, where the tool message does not carry its own copy.
    function _callArgsOf(list) {
        const callArgs = {}
        for (let k = 0; k < list.length; k++) {
            const tcs = list[k].toolCalls || []
            for (let t = 0; t < tcs.length; t++) {
                if (tcs[t] && tcs[t].id)
                    callArgs[tcs[t].id] = (tcs[t].function && tcs[t].function.arguments) || ""
            }
        }
        return callArgs
    }

    // Transcript rows for store messages [from, to).
    //
    // One builder for the loaded window and for the archive: two would drift, and the archive is
    // supposed to be what the transcript would have shown.
    function _buildRows(list, from, to, callArgs) {
        var rows = []
        var i = from
        while (i < to) {
            const m = list[i]
            const role = m.role || "assistant"
            // Assistant messages with only tool_calls are hidden: the tool card and the final answer
            // carry the information.
            if (role === "assistant" && (m.text || "") === "" && (m.toolCalls || []).length > 0) {
                i++
                continue
            }
            // Consecutive tool results merge into one row (models often call several at once).
            if (role === "tool") {
                const group = []
                while (i < to && (list[i].role || "assistant") === "tool") {
                    group.push({
                        "callId": list[i].toolCallId || "",
                        "msgId": list[i].id || "",
                        "name": list[i].name || "",
                        "args": list[i].toolArgs || callArgs[list[i].toolCallId] || "",
                        "output": list[i].text || "",
                        "running": false
                    })
                    i++
                }
                rows.push({
                    // This row may merge several tool results; record them all so a search hit maps
                    // back to it.
                    msgIds: group.map(g => g.msgId).join(","),
                    isUser: false,
                    kind: "tool",
                    toolName: "",
                    toolCallId: "",
                    toolsJson: JSON.stringify(group),
                    status: stDone,
                    rawText: "",
                    runId: "",
                    elapsedMs: 0,
                    attachments: "",
                    reasoning: "",
                    usageIn: 0,
                    usageOut: 0,
                    truncated: false
                })
                continue
            }
            rows.push({
                // Jumps map by messageId, not index: rows merge tool results and drop empty
                // assistant messages, so the two orders differ.
                msgIds: m.id || "",
                isUser: role === "user",
                kind: "text",
                toolName: "",
                toolCallId: "",
                toolsJson: "[]",
                status: stDone,
                rawText: m.text || "",
                runId: "",
                elapsedMs: m.elapsedMs || 0,
                attachments: (m.attachments || []).join("\n"),
                reasoning: m.reasoning || "",
                // lastUsage is the raw last-request report; older sessions only have the cumulative
                // usage, which beats showing 0.
                usageIn: ((m.lastUsage || m.usage) && (m.lastUsage || m.usage).input) || 0,
                usageOut: ((m.lastUsage || m.usage) && (m.lastUsage || m.usage).output) || 0,
                truncated: m.truncated === true
            })
            i++
        }
        return rows
    }

    // Build the archive a chunk at a time, the way a load does: a long prefix is hundreds of rows, and
    // putting them all into the model in one turn of the event loop is what freezes the window.
    function buildArchive() {
        if (_archivedBuilt)
            return
        // Nothing to build *yet* is not "built": called before a load finishes (the card's tab can be
        // clicked at any moment) setting the flag here would leave the archive permanently empty.
        // A live fold with no _archivedMessages behind it still counts, via the queue.
        if (_archivedMessages.length === 0 && _queuedFolded.length === 0)
            return
        _archivedBuilt = true
        const rows = _buildRows(_archivedMessages, 0, _archivedMessages.length, _archivedCallArgs)
        var at = 0
        function chunk() {
            const end = Math.min(at + _loadChunk, rows.length)
            for (let i = at; i < end; i++)
                archived.append(rows[i])
            at = end
            if (at < rows.length) {
                Qt.callLater(chunk)
                return
            }
            // Rows folded while the build was running are older than nothing and newer than the rest.
            for (let q = 0; q < _queuedFolded.length; q++)
                archived.append(_queuedFolded[q])
            _queuedFolded = []
        }
        chunk()
    }

    // Appends rows a chunk at a time, then finishes the load.
    //
    // token is checked on every chunk: switching sessions mid-load must not leave the first session's
    // rows half-inserted under the second session's title.
    function _insertRows(rows, from, token, done) {
        if (token !== _loadToken)
            return
        _pendingRows = rows
        _pendingDone = done
        const end = Math.min(from + _loadChunk, rows.length)
        for (let i = from; i < end; i++)
            messages.append(rows[i])
        // Where the next insert resumes — which is also where _finishRows would resume, and it
        // appends from here. Leaving it at `from` made the last chunk land twice.
        _pendingFrom = end
        if (end < rows.length) {
            Qt.callLater(function () { _insertRows(rows, end, token, done) })
            return
        }
        _finishRows()
    }

    // Inserts everything still queued, in one turn. Used by the chunked path when it runs out, and by
    // anything that needs the transcript complete right now (a send must not land among rows that have
    // not been written yet).
    function _finishRows() {
        const rows = _pendingRows
        for (let i = _pendingFrom; i < rows.length; i++)
            messages.append(rows[i])
        _pendingRows = []
        _pendingFrom = 0
        const done = _pendingDone
        _pendingDone = null
        loading = false
        rowsLoaded()
        _setCompaction(_pendingCompaction)
        // The daemon may still be mid-reply for this session — the window reloaded while a turn was
        // running, or the turn came from the CLI. Say so, or the composer shows "send" while text is
        // arriving and nothing on screen moves.
        if (_pendingBusy)
            _adoptRunningTurn()
        if (done) done()
        locateJump()
    }

    property bool _pendingBusy: false
    property string _pendingRunningText: ""
    property string _pendingRunningReasoning: ""
    // Epoch ms of the running turn's start, from the daemon; 0 when it is not running.
    property double _pendingStartedAt: 0

    // Join a turn the daemon is already producing.
    //
    // The step in flight is not on disk yet — only the steps that finished are — so the rows just
    // loaded end at the last completed step. A fresh streaming row gives the incoming text somewhere
    // to land; whatever that step had already written before the reload arrives with its run.finished,
    // which carries the persisted text.
    function _adoptRunningTurn() {
        const rid = Agent.adopt(Sessions.activeId)
        if (rid === "")
            return
        activeRunId = rid
        // The turn's real age, not the age of this window's subscription: the daemon ran it and reports
        // when it began, so a reply that has been coming for a minute does not claim to be 1s old.
        runStartedAt = _pendingStartedAt > 0 ? _pendingStartedAt : Date.now()
        runElapsedMs = Math.max(0, Date.now() - runStartedAt)
        _newStep(rid)
        // Start the bubble with what the running step has already written. The persisted rows end at
        // the last finished step, so this prefix is not anywhere in the transcript — and the daemon
        // reports it because only it holds it. It is replaced wholesale by run.finished.
        if (_pendingRunningText !== "" || _pendingRunningReasoning !== "") {
            messages.set(liveIndex, {
                rawText: _pendingRunningText,
                reasoning: _pendingRunningReasoning
            })
        }
        _pendingRunningText = ""
        _pendingRunningReasoning = ""
        _pendingStartedAt = 0
    }

    // ---------- edit / regenerate / branch ----------

    // Edit a user message: overwrite it, drop everything after it, then regenerate.
    function editMessage(index, text) {
        if (busy) return false
        if (index < 0 || index >= messages.count) return false
        if (!messages.get(index).isUser) return false
        var t = String(text || "").trim()
        if (t === "") return false
        // Addressed by id: a row index is not a session.messages index.
        var mid = messageIdAt(index)
        if (mid === "")
            return missingId(index)

        Backend.sendRequest("session.rewrite", {
            "id": Sessions.activeId,
            "messageId": mid,
            "text": t
        }, function (res, err) {
            if (err) {
                ToastService.info(err)
                return
            }
            // The daemon already dropped everything after this message, and the rows below it here are
            // that same content. Reloading the session to rediscover it re-reads and re-lays-out the
            // whole transcript — seconds on a long one — for a change that is one row deep.
            messages.setProperty(index, "rawText", t)
            chat._dropRowsAfter(index)
            chat._startRegenerate()
        })
        return true
    }

    // Regenerate a reply: truncate after its prompt, then run again.
    function regenerate(index) {
        if (busy) return false
        let keep = -1
        for (let i = Math.min(index, messages.count - 1); i >= 0; i--) {
            if (messages.get(i).isUser) {
                keep = i
                break
            }
        }
        if (keep < 0) return false
        const mid = messageIdAt(keep)
        if (mid === "")
            return missingId(keep)

        Backend.sendRequest("session.truncate", {
            "id": Sessions.activeId,
            "messageId": mid
        }, function (res, err) {
            if (err) {
                ToastService.info(err)
                return
            }
            chat._dropRowsAfter(keep)
            chat._startRegenerate()
        })
        return true
    }

    // Removes every row below one, which is what truncate and rewrite do in the daemon.
    //
    // The compaction seam has to be re-found: if its cut point was one of the dropped messages the
    // summary no longer marks anything, and the daemon drops the record for the same reason.
    function _dropRowsAfter(row) {
        if (row < 0 || row >= messages.count - 1) {
            if (compaction)
                _locateCompaction()
            return
        }
        messages.remove(row + 1, messages.count - row - 1)
        if (compaction) {
            _locateCompaction()
            if (compactionRow < 0)
                _setCompaction(null)
        }
    }

    // Fork into a new session up to and including index, then switch to it.
    function branch(index) {
        if (Sessions.activeId === "") return false
        var mid = lastMessageIdAt(index)
        if (mid === "")
            return missingId(index)
        Backend.sendRequest("session.branch", {
            "id": Sessions.activeId,
            "messageId": mid
        }, function (res, err) {
            if (err || !res || !res.session) {
                ToastService.info(err || I18n.tr("Could not create the branch", "branch creation failed"));
                return
            }
            Sessions.reload()
            Sessions.select(res.session.id)
            ToastService.info(I18n.tr("Branch created", "branch created toast"));
        })
        return true
    }

    // Append an empty assistant message, then have the daemon rerun the last user message.
    function _startRegenerate() {
        if (Sessions.activeId === "") return
        var rid = "run-" + Date.now()
        activeRunId = rid
        runStartedAt = Date.now()
        runElapsedMs = 0
        messages.append({
            msgIds: "",
            isUser: false,
            kind: "text",
            toolName: "",
            toolCallId: "",
            status: stStreaming,
            rawText: "",
            runId: rid,
            elapsedMs: 0,
            attachments: "",
            usageIn: 0,
            usageOut: 0,
            truncated: false,
            reasoning: "",
            toolsJson: "[]"
        })
        liveIndex = messages.count - 1
        Agent.regenerate(rid)
    }

    // Deltas carry only the new chunk, so append; replacing would leave the last ~50 ms only.
    function _setLive(rid, chunk) {
        if (rid !== activeRunId) return
        // Text means the fold is over, whether or not its own event arrived (a failed fold publishes
        // nothing at all).
        compacting = false
        if (liveIndex < 0 || liveIndex >= messages.count) return
        const prev = messages.get(liveIndex).rawText || ""
        messages.set(liveIndex, { rawText: prev + chunk, status: stStreaming })
    }

    // Thinking has its own channel: it arrives first and must stay out of the answer.
    function _setReasoning(rid, chunk) {
        if (rid !== activeRunId) return
        if (liveIndex < 0 || liveIndex >= messages.count) return
        const prev = messages.get(liveIndex).reasoning || ""
        messages.set(liveIndex, { reasoning: prev + chunk })
    }

    function _setReasoningText(rid, text) {
        if (rid !== activeRunId) return
        if (liveIndex < 0 || liveIndex >= messages.count) return
        messages.set(liveIndex, { reasoning: text })
    }

    // One bubble spins at a time. A step ends or a tool call starts once its text is final;
    // leaving it running shows an empty bubble, since models often emit no text when calling
    // tools.
    function _closeLive() {
        if (liveIndex < 0 || liveIndex >= messages.count) return
        if (messages.get(liveIndex).status !== stStreaming) return
        messages.set(liveIndex, { status: stDone })
    }

    // One run can call the model several times; each step gets its own bubble so tool text
    // and the answer do not merge.
    // previousMessageId is the persisted id of the bubble being closed (see Agent.stepStarted). A live
    // row is created without one — the daemon mints it when the step is written — and every message
    // action is addressed by id, so it has to be adopted here or the bubble can never be branched,
    // regenerated or truncated.
    function _newStep(rid, previousMessageId) {
        if (rid !== activeRunId) return
        // An adopted turn opens a placeholder row before the stream resumes; if its first event is
        // "a new step started", that placeholder is the row this step belongs in. Closing it instead
        // would leave an empty bubble at the top of the reply.
        if (liveIndex >= 0 && liveIndex < messages.count) {
            const live = messages.get(liveIndex)
            if (live.runId === rid && live.status === stStreaming && live.kind === "text"
                    && String(live.rawText || "") === "" && String(live.reasoning || "") === "")
                return
        }
        _adoptMessageId(liveIndex, previousMessageId)
        _closeLive()
        messages.append({
            msgIds: "",
            isUser: false,
            kind: "text",
            toolName: "",
            toolCallId: "",
            status: stStreaming,
            rawText: "",
            runId: rid,
            elapsedMs: 0,
            attachments: "",
            usageIn: 0,
            usageOut: 0,
            truncated: false,
            reasoning: "",
            toolsJson: "[]"
        })
        liveIndex = messages.count - 1
    }

    // Consecutive tool calls share one card: one card per call would stretch the thread and
    // make the output unreadable.
    function _toolStarted(rid, callId, name, args) {
        if (rid !== activeRunId) return
        // A tool call means this step's text is complete.
        _closeLive()
        var item = {
            "callId": callId,
            "name": name,
            "args": String(args || ""),
            "output": "",
            "running": true
        }
        var lastIndex = messages.count - 1
        if (lastIndex >= 0) {
            const last = messages.get(lastIndex)
            if (last.kind === "tool" && last.runId === rid) {
                const list = _parseTools(last.toolsJson)
                list.push(item)
                messages.set(lastIndex, {
                    "toolsJson": JSON.stringify(list),
                    "status": stStreaming
                })
                liveIndex = lastIndex
                return
            }
        }
        messages.append({
            msgIds: "",
            isUser: false,
            kind: "tool",
            toolName: "",
            toolCallId: "",
            toolsJson: JSON.stringify([item]),
            status: stStreaming,
            rawText: "",
            runId: rid,
            elapsedMs: 0,
            attachments: "",
            reasoning: "",
            usageIn: 0,
            usageOut: 0,
            truncated: false
        })
        liveIndex = messages.count - 1
    }

    // ListModel turns JS arrays into QQmlListModel, so groups are stored as JSON strings and
    // parsed back on use.
    function _parseTools(json) {
        try {
            const a = JSON.parse(json || "[]")
            return (a instanceof Array) ? a : []
        } catch (e) {
            return []
        }
    }

    function _toolFinished(rid, callId, output) {
        if (rid !== activeRunId) return
        for (let i = messages.count - 1; i >= 0; i--) {
            const row = messages.get(i)
            if (row.kind !== "tool") continue
            const list = _parseTools(row.toolsJson)
            let hit = -1
            for (let j = 0; j < list.length; j++) {
                if (list[j].callId === callId) hit = j
            }
            if (hit < 0) continue
            const next = list.slice()
            next[hit] = {
                "callId": callId,
                "name": next[hit].name,
                "args": next[hit].args,
                "output": String(output || ""),
                "running": false
            }
            let anyRunning = false
            for (let k = 0; k < next.length; k++) {
                if (next[k].running) anyRunning = true
            }
            messages.set(i, {
                "toolsJson": JSON.stringify(next),
                "status": anyRunning ? stStreaming : stDone
            })
            return
        }
    }

    // One reply = the assistant text from this message back to the previous user message.
    // Tool steps are process, not content, so "copy" at the end of a reply yields the whole
    // reply rather than the last chunk.
    function replyText(index) {
        let start = index;
        while (start > 0) {
            const prev = messages.get(start - 1);
            if (!prev || prev.isUser)
                break;
            start--;
        }
        let out = "";
        for (let i = start; i <= index; i++) {
            const m = messages.get(i);
            if (!m || m.isUser || m.kind === "tool")
                continue;
            const t = String(m.rawText || "").trim();
            if (t === "")
                continue;
            out += (out === "" ? "" : "\n\n") + t;
        }
        return out;
    }

    // Per-message usage: the reply row uses it to say how many tokens the output cap cut off. The
    // session total (the chip) comes from the daemon instead — see sessionTokens.
    function _setUsage(rid, input, output) {
        if (rid !== activeRunId) return
        if (liveIndex < 0 || liveIndex >= messages.count) return
        // Usage belongs to the text reply; skip it when liveIndex points at a tool card.
        if (messages.get(liveIndex).kind === "tool") return
        messages.set(liveIndex, { usageIn: input || 0, usageOut: output || 0 })
    }

    // The persisted reply is authoritative; reconcile against it so a dropped delta does not
    // leave a gap.
    function _settleText(rid, text) {
        if (rid !== activeRunId) return
        if (liveIndex < 0 || liveIndex >= messages.count) return
        if (messages.get(liveIndex).kind === "tool") return
        if (typeof text !== "string" || text === "") return
        messages.set(liveIndex, { rawText: text })
    }

    function _settle(rid, cancelled, truncated, messageId) {
        if (rid !== activeRunId) return
        compacting = false
        var elapsed = runStartedAt > 0 ? Date.now() - runStartedAt : runElapsedMs
        var finalStatus = cancelled ? stCancelled : stDone
        // liveIndex is cleared below; the placeholder check needs it afterwards.
        var settled = liveIndex

        // Finish every bubble of this run, not just liveIndex: one run produces one bubble per
        // step, and this also covers a run interrupted before its tool reported completion.
        for (let i = messages.count - 1; i >= 0; i--) {
            const it = messages.get(i)
            if (it.runId !== rid || it.status !== stStreaming) continue
            messages.set(i, { status: finalStatus })
        }

        // The bubble's status is not the tool's. A call that never reported completion — the run was
        // cancelled while it was blocked, or the question it was blocked on went away with the
        // connection — keeps `running: true` inside its group. Left there the card spins forever, and
        // an `ask_user` left running goes on offering "Show the question" for a question nobody is
        // waiting on.
        for (let t = messages.count - 1; t >= 0; t--) {
            const toolRow = messages.get(t)
            if (toolRow.runId !== rid || toolRow.kind !== "tool") continue
            const group = _parseTools(toolRow.toolsJson)
            let changed = false
            for (let u = 0; u < group.length; u++) {
                if (!group[u] || group[u].running !== true) continue
                group[u] = {
                    "callId": group[u].callId,
                    "name": group[u].name,
                    "args": group[u].args,
                    "output": group[u].output,
                    "running": false
                }
                changed = true
            }
            if (changed)
                messages.set(t, { "toolsJson": JSON.stringify(group) })
        }

        if (liveIndex >= 0 && liveIndex < messages.count)
            messages.set(liveIndex, {
                status: finalStatus,
                elapsedMs: elapsed,
                truncated: truncated === true
            })
        // The reply is on disk now, so the bubble finally has an id; branching needs one.
        _adoptMessageId(settled, messageId === undefined ? "" : messageId)
        activeRunId = ""
        liveIndex = -1
        runStartedAt = 0
        runElapsedMs = elapsed
        // An adopted turn that turns out to be over leaves its placeholder behind: an empty bubble
        // that reads as a reply which said nothing.
        _dropEmptyLiveRow(settled)
    }

    // Gives a live row the id of the message it ended up as.
    //
    // Only when it has none: a reloaded row already carries its own, and overwriting it would point the
    // row at someone else's message. "" means "unknown", which leaves the row exactly as it was.
    function _adoptMessageId(index, messageId) {
        if (!messageId || index < 0 || index >= messages.count)
            return
        if (String(messages.get(index).msgIds || "") !== "")
            return
        messages.set(index, { "msgIds": messageId })
    }

    // Removes the row at index if it never received anything.
    function _dropEmptyLiveRow(index) {
        if (index < 0 || index >= messages.count)
            return
        const row = messages.get(index)
        if (row.kind !== "text" || row.status === stStreaming)
            return
        if (String(row.rawText || "") !== "" || String(row.reasoning || "") !== "")
            return
        messages.remove(index)
        if (compaction)
            _locateCompaction()
    }

    readonly property Connections _link: Connections {
        target: Agent

        function onReasoning(rid, text) {
            chat._setReasoning(rid, text)
        }

        function onFinalReasoning(rid, text) {
            chat._setReasoningText(rid, text)
        }

        function onDelta(rid, text) {
            chat._setLive(rid, text)
        }

        function onStepStarted(rid, step, previousMessageId) {
            chat._newStep(rid, previousMessageId)
        }

        function onToolStarted(rid, callId, name, args) {
            chat._toolStarted(rid, callId, name, args)
        }

        function onToolFinished(rid, callId, output) {
            chat._toolFinished(rid, callId, output)
        }

        function onUsage(rid, input, output) {
            chat._setUsage(rid, input, output)
        }

        // Absolute, from the daemon: assign, never add.
        function onSessionUsage(total) {
            chat.sessionTokens = total
        }

        function onFinalText(rid, text) {
            chat._settleText(rid, text)
        }

        function onCompacting(sessionId, count) {
            chat._compacting(sessionId, count)
        }

        function onCompacted(sessionId, record) {
            chat._compacted(sessionId, record)
        }

        // chat.send answered with the persisted user message id; attach it so the row can be
        // edited or regenerated without waiting for a reload.
        function onSent(rid, messageId) {
            if (rid !== chat.activeRunId || messageId === "")
                return
            if (chat.pendingUserRow >= 0 && chat.pendingUserRow < chat.messages.count)
                chat.messages.set(chat.pendingUserRow, { "msgIds": messageId })
            chat.pendingUserRow = -1
        }

        function onFinished(rid, cancelled, truncated, messageId) {
            chat._settle(rid, cancelled, truncated, messageId)
        }

        function onFailed(rid, message) {
            // Finish on failure too, leaving the message "interrupted"; the error goes to a toast.
            ToastService.show(message);
        }
    }

    // Flush the queued message once the session exists.
    readonly property Connections _sessionLink: Connections {
        target: Sessions

        function onActiveIdChanged() {
            if (chat.pendingSend) {
                const p = chat.pendingSend
                chat.pendingSend = null
                chat.send(p.text, p.attachments)
            }
        }
    }
}
