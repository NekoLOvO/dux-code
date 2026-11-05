import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.DCommon.Common
import qs.DCommon.Widgets
import qs.Widgets
import qs.Modules.Blocks
import "../Common/Blockizer.js" as Blockizer
import "../Common/TextFmt.js" as TextFmt

Item {
    id: root

    property bool isUser: false
    property int status: 0
    property string rawText: ""
    property int elapsedMs: 0
    // Attached files, one absolute path per line (ListModel stores plain strings).
    property string attachments: ""
    // "text" | "tool": a tool call is not conversation content, so it is drawn as a card.
    property string kind: "text"
    property string toolName: ""
    property string toolCallId: ""
    // This reply was cut off by the output limit (finish_reason == length). It is almost never
    // a complete answer, so say so and offer a way to continue.
    property bool truncated: false
    // Output tokens of the truncated turn (= the limit in effect), used to say where it stopped.
    property int usageOut: 0
    // Consecutive tool calls. A row can hold several (models often call a few at once); each is
    // { callId, name, args, output, running }.
    //
    // Stored as a JSON string: ListModel converts arrays to QQmlListModel, which has no Array
    // methods (.some/.slice throw).
    property string toolsJson: "[]"
    readonly property var tools: {
        try {
            var a = JSON.parse(toolsJson || "[]");
            return (a instanceof Array) ? a : [];
        } catch (e) {
            return [];
        }
    }
    // Tool card states, same rule as thinking blocks: only "auto" looks at whether the call is
    // still running.
    property bool toolTouched: false
    property bool toolOpen: false
    readonly property bool toolExpanded: toolTouched ? toolOpen : (SettingsData.toolDisplay === "open" ? true : (SettingsData.toolDisplay === "closed" ? false : toolCard.anyRunning))

    function toggleTools() {
        toolOpen = !toolExpanded;
        toolTouched = true;
    }
    // Model reasoning. Produced like the answer but never part of the body; dropping it means
    // paying for output nobody sees.
    property string reasoning: ""
    // Highlighted when arriving from a search hit (MessageList clears it on a timer).
    property bool highlighted: false
    // This row is where a compaction takes over: everything above it is no longer sent to the model
    // and a summary stands in for it. The divider is drawn above this message rather than as a row
    // of its own, so a transcript keeps its own rows and nothing has to be re-indexed.
    property bool showCompaction: false
    property int compactionCount: 0
    property string compactionSummary: ""
    // Once the user toggled it by hand, respect that and stop following the setting.
    property bool thinkingTouched: false
    property bool thinkingOpen: false
    // "Thinking" means: has reasoning, no body yet, still streaming.
    readonly property bool thinkingActive: reasoning !== "" && rawText === "" && streaming
    readonly property bool thinkingExpanded: thinkingTouched ? thinkingOpen : (SettingsData.thinkingDisplay === "open" ? true : (SettingsData.thinkingDisplay === "closed" ? false : thinkingActive))

    // One human-readable line for the tool arguments: bash keys on command, file reads on path,
    // everything else shows the raw JSON.
    function toolCommand(raw) {
        const s = String(raw || "").trim();
        if (s === "")
            return "";
        try {
            const o = JSON.parse(s);
            if (o && typeof o.command === "string")
                return "$ " + o.command;
            if (o && typeof o.path === "string")
                return o.path;
            return JSON.stringify(o);
        } catch (e) {
            return s;
        }
    }

    function toggleThinking() {
        thinkingOpen = !thinkingExpanded;
        thinkingTouched = true;
    }

    // Message actions are handled outside: one dialog for the list, not a Popup per message.
    signal editRequested(int index, string text)
    signal regenerateRequested(int index)
    signal branchRequested(int index)
    signal continueRequested(int index)

    readonly property int stStreaming: 1
    readonly property int stCancelled: 2
    readonly property int maxColumn: 820
    // Side gutter so text never touches the sidebar or window edge when the window is narrow.
    // Matches the composer inset and leaves room for the scrollbar.
    readonly property int sideGutter: Theme.spacingXL
    readonly property real columnWidth: Math.max(0, Math.min(root.width - root.sideGutter * 2, root.maxColumn))

    readonly property var attachmentList: {
        const s = String(attachments || "");
        if (s === "")
            return [];
        return s.split("\n").filter(p => p !== "");
    }

    // Attachments show the filename only (a full path in a bubble is noise); the tooltip has the path.
    readonly property string attachmentLabel: {
        const names = attachmentList.map(p => {
            const i = p.lastIndexOf("/");
            return i >= 0 ? p.slice(i + 1) : p;
        });
        return names.length > 0 ? "📎 " + names.join(" · ") : "";
    }

    property var blocks: []
    property int rev: 0
    readonly property int blockCount: blocks.length

    width: ListView.view ? ListView.view.width : 600
    height: column.height

    component ActionButton: Rectangle {
        id: btn

        property string iconName: ""
        property bool danger: false

        signal activated

        width: 28
        height: 28
        radius: Theme.cornerRadiusS
        color: "transparent"

        StyledText {
            anchors.centerIn: parent
            text: btn.iconName
            font.family: Fonts.icons
            font.pixelSize: Theme.iconSizeMedium
            color: btn.danger ? Theme.error : Theme.surfaceVariantText
        }

        StateLayer {
            stateColor: btn.danger ? Theme.error : Theme.surfaceText
            cornerRadius: btn.radius
            onClicked: btn.activated()
        }
    }

    // Reasoning goes through the same block parser.
    //
    // Code inside reasoning is a draft for the model, but the user reads it, so it must render.
    // Exception: `tool` is our own protocol and would be mistaken for a tool card.
    property var reasoningBlocks: []
    property int reasoningRev: 0

    function reasoningBlockAt(i) {
        reasoningRev;
        return reasoningBlocks[i];
    }

    function reparseReasoning() {
        var list;
        try {
            list = Blockizer.parse(reasoning);
        } catch (e) {
            list = [{
                "type": "p",
                "text": reasoning
            }];
        }
        for (var i = 0; i < list.length; i++) {
            if (list[i] && list[i].type === "tool") {
                list[i] = {
                    "type": "code",
                    "lang": "",
                    "code": list[i].code || "",
                    "closed": true
                };
            }
        }
        reasoningBlocks = Blockizer.groupProse(list);
        reasoningRev++;
    }

    function reparse() {
        try {
            // Prose becomes one block per run, so the whole run lives in a single TextEdit and a
            // selection can cross paragraph, heading and list boundaries.
            blocks = Blockizer.groupProse(Blockizer.parse(rawText));
        } catch (e) {
            blocks = [{ type: "p", text: rawText }];
        }
        rev++;
    }

    function blockAt(i) {
        rev;
        return blocks[i];
    }

    // Number of tool calls = "N steps".
    // Whether this is the end of the whole reply.
    //
    // One reply is split into many messages (text -> tool -> text). Actions (regenerate / copy
    // / branch) address the whole reply, so they only appear under the final one.
    readonly property bool replyEnd: {
        const total = Chat.messages.count;   // depends on count so it recomputes on new messages
        if (index + 1 >= total)
            return true;
        const next = Chat.messages.get(index + 1);
        return !next || next.isUser === true;
    }

    // The ask_user call the daemon is blocked on, whose dialog the user put aside.
    //
    // No id plumbing: the daemon only publishes tool.finished once the answer arrives, so while a
    // question is pending its tool stays "running". The newest such call is the blocked one.
    //
    // A question that is no longer pending voids the button whatever the tool says: the answer may
    // have been given from the CLI, or the connection that carried it may be gone.
    readonly property bool questionWaiting: {
        if (!Chat.questionHidden || Backend.questions.length === 0)
            return false;
        const items = root.tools || [];
        for (let i = items.length - 1; i >= 0; i--) {
            if (items[i] && items[i].name === "ask_user" && items[i].running === true)
                return true;
        }
        return false;
    }

    // Plain-text version of the tool card (command + output) for copying.
    function toolPlainText() {
        const list = root.tools || [];
        const out = [];
        for (let i = 0; i < list.length; i++) {
            const t = list[i];
            let head = t.name || "";
            if (t.args !== undefined && t.args !== null) {
                head += " " + (typeof t.args === "string" ? t.args : JSON.stringify(t.args));
            }
            const body = String(t.output || "").trim();
            out.push(body === "" ? head : head + "\n" + body);
        }
        return out.join("\n\n");
    }

    readonly property int steps: {
        rev;
        let n = 0;
        for (let i = 0; i < blocks.length; i++) {
            if (blocks[i].type === "tool")
                n++;
        }
        return n;
    }

    readonly property bool streaming: status === stStreaming
    // Show the status row only while streaming, cancelled, or when tools actually ran; a plain
    // text answer should not gain a line.
    readonly property bool showStatus: !isUser && (streaming || status === stCancelled || steps > 0)

    readonly property string elapsedText: {
        const ms = streaming ? (Chat.runStartedAt > 0 ? Chat.runElapsedMs : 0) : elapsedMs;
        if (!ms || ms < 500)
            return "";
        return Sessions.formatElapsed(ms);
    }

    readonly property string statusText: {
        if (streaming)
            return elapsedText !== "" ? I18n.tr("Working", "streaming status, agent is running") + " · " + elapsedText : I18n.tr("Working", "streaming status, agent is running");
        if (status === stCancelled)
            return elapsedText !== "" ? I18n.tr("Cancelled", "streaming status, run was cancelled") + " · " + elapsedText : I18n.tr("Cancelled", "streaming status, run was cancelled");
        if (steps > 0) {
            const label = steps === 1 ? I18n.tr("Ran 1 step", "finished status, exactly one tool call") : I18n.tr("Ran %1 steps", "finished status with tool call count, %1 is the number").arg(steps);
            return elapsedText !== "" ? label + " · " + elapsedText : label;
        }
        return "";
    }

    Component.onCompleted: {
        reparse();
        reparseReasoning();
    }
    onRawTextChanged: reparse()
    onReasoningChanged: reparseReasoning()

    Column {
        id: column

        width: root.columnWidth
        anchors.horizontalCenter: parent.horizontalCenter
        spacing: Theme.spacingM

        // The seam where compaction takes over: this row is the first message still sent verbatim,
        // and everything above it is a summary. Drawn above the message so the boundary reads as
        // "from here on", not as part of this message.
        CompactionDivider {
            id: divider

            width: parent.width
            visible: root.showCompaction
            height: visible ? implicitHeight : 0
            count: root.compactionCount
            summary: root.compactionSummary
        }

        // ---------- assistant ----------
        Column {
            id: col
            width: parent.width
            spacing: Theme.spacingM
            visible: !root.isUser && root.kind !== "tool"

            // ---- status row (pinned to the top of the output) ----
            Row {
                id: statusRow

                visible: root.showStatus
                width: col.width
                height: visible ? Math.max(iconLabel.height, statusBadge.implicitHeight) : 0
                spacing: Theme.spacingS

                // Spinner is the library's DSpinner; arc width, speed and easing are its business.
                DSpinner {
                    anchors.verticalCenter: parent.verticalCenter
                    visible: root.streaming
                    size: Theme.iconSizeSmall
                    color: Theme.primary
                }

                StyledText {
                    id: iconLabel

                    anchors.verticalCenter: parent.verticalCenter
                    visible: !root.streaming
                    text: root.status === root.stCancelled ? "stop_circle" : "check_circle"
                    font.family: Fonts.icons
                    font.pixelSize: Theme.iconSizeSmall
                    color: root.status === root.stCancelled ? Theme.surfaceVariantText : Theme.success
                }

                DBadge {
                    id: statusBadge

                    anchors.verticalCenter: parent.verticalCenter
                    text: root.statusText
                    color: root.streaming ? Theme.primary : Theme.surfaceContainerHighest
                    textColor: root.streaming ? Theme.primaryText : Theme.surfaceVariantText
                }
            }

            // ---- reasoning ----
            // Expanded while generating so you can watch it think, collapsed once the answer
            // starts. One setting controls this.
            Item {
                id: thinkingSection

                width: col.width
                height: (root.reasoning !== "" && thinkingColumn.visible) ? thinkingColumn.implicitHeight : 0
                visible: root.reasoning !== ""

                Column {
                    id: thinkingColumn

                    width: parent.width
                    spacing: Theme.spacingXS
                    visible: root.reasoning !== ""

                    Rectangle {
                        id: thinkingHeader

                        width: parent.width
                        height: Theme.buttonHeightXS
                        radius: Theme.cornerRadiusS
                        color: thinkingHover.hovered ? Theme.surfaceContainerHigh : "transparent"

                        HoverHandler {
                            id: thinkingHover
                            cursorShape: Qt.PointingHandCursor
                        }

                        Row {
                            anchors.left: parent.left
                            anchors.leftMargin: Theme.spacingS
                            anchors.verticalCenter: parent.verticalCenter
                            spacing: Theme.spacingS

                            StyledText {
                                anchors.verticalCenter: parent.verticalCenter
                                text: "psychology"
                                font.family: Fonts.icons
                                font.pixelSize: Theme.iconSizeSmall
                                color: Theme.surfaceVariantText
                            }

                            StyledText {
                                anchors.verticalCenter: parent.verticalCenter
                                text: root.thinkingActive ? I18n.tr("Thinking…", "label while the model is still reasoning") : I18n.tr("Thought", "label of a finished reasoning block")
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceVariantText
                            }

                            StyledText {
                                anchors.verticalCenter: parent.verticalCenter
                                text: root.thinkingExpanded ? "expand_less" : "expand_more"
                                font.family: Fonts.icons
                                font.pixelSize: Theme.iconSizeSmall
                                color: Theme.surfaceVariantText
                            }
                        }

                        MouseArea {
                            anchors.fill: parent
                            cursorShape: Qt.PointingHandCursor
                            onClicked: root.toggleThinking()
                        }

                        // Declared after the MouseArea, otherwise a click becomes expand/collapse.
                        CopyButton {
                            visible: thinkingHover.hovered
                            anchors.right: parent.right
                            anchors.rightMargin: Theme.spacingXS
                            anchors.verticalCenter: parent.verticalCenter
                            copyText: root.reasoning
                            Accessible.name: I18n.tr("Copy the reasoning", "message action: copy the thinking block")
                        }
                    }

                    Column {
                        id: reasoningColumn

                        x: Theme.spacingS
                        width: parent.width - Theme.spacingS * 2
                        spacing: Theme.spacingXS
                        visible: root.thinkingExpanded

                        Repeater {
                            model: root.reasoningBlocks.length

                            delegate: BlockItem {
                                width: reasoningColumn.width
                                blk: root.reasoningBlockAt(index)
                                muted: true
                            }
                        }
                    }
                }
            }

            Repeater {
                model: root.blockCount

                delegate: Item {
                    id: blockWrap

                    readonly property var blk: root.blockAt(index)

                    width: col.width
                    height: blockItem.implicitHeight

                    BlockItem {
                        id: blockItem
                        width: parent.width
                        blk: blockWrap.blk
                    }

                    // Copy without aiming at all: a prose run is now one TextEdit and can be
                    // selected freely, so this is a shortcut for the section under the cursor
                    // rather than the only way to take more than one line.
                    CopyButton {
                        visible: blockHover.hovered && blockWrap.blk && blockWrap.blk.type !== "code"
                        anchors.right: parent.right
                        anchors.bottom: parent.bottom
                        anchors.bottomMargin: -Theme.spacingXS
                        // Sits on top of the text, so it needs a background to stay visible.
                        variant: "tonal"
                        // A prose run has no single .text: lists and quotes carry their content in
                        // parts, so the Markdown they came from is rebuilt instead.
                        copyText: blockWrap.blk
                            ? (blockWrap.blk.type === "code"
                                ? (blockWrap.blk.code || "")
                                : Blockizer.proseCopy(blockWrap.blk))
                            : ""
                        Accessible.name: I18n.tr("Copy this block", "message action: copy one paragraph")
                    }

                    HoverHandler {
                        id: blockHover
                    }
                }
            }

            // ---- cut off by the output limit ----
            // Not "finished": the model was cut at max_tokens. Without saying so, the user sees a
            // sentence that just stops and reads it as a conclusion.
            Row {
                id: truncatedRow

                visible: root.truncated && !root.streaming
                width: col.width
                height: visible ? Math.max(cutLabel.implicitHeight, continueBtn.height) : 0
                spacing: Theme.spacingS

                StyledText {
                    id: cutIcon

                    anchors.verticalCenter: parent.verticalCenter
                    text: "content_cut"
                    font.family: Fonts.icons
                    font.pixelSize: Theme.iconSizeSmall
                    color: Theme.warning
                }

                StyledText {
                    id: cutLabel

                    anchors.verticalCenter: parent.verticalCenter
                    width: parent.width - cutIcon.width - continueBtn.width - parent.spacing * 2
                    text: root.usageOut > 0
                        ? I18n.tr("Reached the output limit (%1 tokens) — this reply was cut off.", "note under a reply cut off by the output token cap, with the token count").arg(root.usageOut)
                        : I18n.tr("Reached the output limit — this reply was cut off.", "note under a reply cut off by the output token cap")
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                    wrapMode: Text.WordWrap
                }

                // DButton supplies ripple, press scale and hover; a hand-drawn Rectangle reads as unresponsive.
                DButton {
                    id: continueBtn

                    anchors.verticalCenter: parent.verticalCenter
                    text: I18n.tr("Continue", "button under a truncated reply that asks the model to keep going")
                    buttonHeight: Theme.buttonHeightXS
                    minimumWidth: 0
                    // "tonal", not the default "filled": the filled background is the send button's
                    // color, and a second one here would read as the primary action. The variant pairs
                    // the background with its own text color; writing the pair by hand got it wrong once.
                    variant: "tonal"
                    onClicked: root.continueRequested(index)
                }
            }

            // ---- action row (space reserved; only fades, so text never shifts) ----
            Item {
                id: actionSlot

                width: col.width
                height: (root.rawText.length > 0 && root.replyEnd) ? 28 : 0

                Row {
                    id: actionBar

                    anchors.left: parent.left
                    anchors.verticalCenter: parent.verticalCenter
                    spacing: Theme.spacingXS
                    opacity: (hover.hovered && !root.streaming) ? 1 : 0
                    enabled: hover.hovered && !root.streaming

                    Behavior on opacity {
                        enabled: Theme.animationsEnabled

                        NumberAnimation {
                            duration: Theme.expressiveDurations.expressiveEffects
                            easing.type: Easing.BezierSpline
                            easing.bezierCurve: Theme.expressiveCurves.standard
                        }
                    }

                ActionButton {
                    iconName: "refresh"
                    Accessible.name: I18n.tr("Regenerate", "message action: regenerate this reply")
                    enabled: !Chat.busy
                    onActivated: root.regenerateRequested(index)
                }

                // Copies the whole reply, not just this last message.
                CopyButton {
                    copyText: Chat.replyText(index)
                    Accessible.name: I18n.tr("Copy", "message action: copy this reply")
                }

                ActionButton {
                    iconName: "call_split"
                    Accessible.name: I18n.tr("Branch", "message action: branch the conversation here")
                    onActivated: root.branchRequested(index)
                }

                }
            }

        }

        // ---------- tool ----------
        // Consecutive tool calls share one card: the header shows what ran and its state, with
        // command and output folded inside.
        DCard {
            id: toolCard

            readonly property var items: root.tools || []
            readonly property bool anyRunning: items.some(function (t) {
                return t.running === true;
            })
            readonly property var uniqueNames: {
                var out = [];
                for (var i = 0; i < items.length; i++) {
                    var n = items[i].name || "";
                    if (n !== "" && out.indexOf(n) === -1)
                        out.push(n);
                }
                return out;
            }
            readonly property string summary: {
                if (items.length === 0)
                    return "";
                if (uniqueNames.length === 1 && items.length > 1)
                    return uniqueNames[0] + " ×" + items.length;
                return uniqueNames.join(" · ");
            }

            // Direct child of the layout column, so width only: a positioner must not manage a child
            // that anchors itself.
            width: parent.width
            height: (root.kind === "tool" && items.length > 0) ? inner.implicitHeight + pad * 2 : 0
            visible: root.kind === "tool" && items.length > 0
            pad: Theme.spacingM
            color: Theme.surfaceContainer
            radius: Theme.cornerRadiusM
            border.width: Theme.outlineWidth
            border.color: anyRunning ? Theme.primary : Theme.outlineVariant

            Column {
                id: inner

                width: parent.width
                spacing: Theme.spacingS

                // Header: icon + tool name (xN) + state + chevron.
                Rectangle {
                    width: parent.width
                    height: Theme.buttonHeightXS
                    radius: Theme.cornerRadiusS
                    color: toolHover.hovered ? Theme.surfaceContainerHigh : "transparent"

                    HoverHandler {
                        id: toolHover
                        cursorShape: Qt.PointingHandCursor
                    }

                    Row {
                        anchors.left: parent.left
                        anchors.verticalCenter: parent.verticalCenter
                        spacing: Theme.spacingS

                        StyledText {
                            anchors.verticalCenter: parent.verticalCenter
                            text: "build"
                            font.family: Fonts.icons
                            font.pixelSize: Theme.iconSizeSmall
                            color: toolCard.anyRunning ? Theme.primary : Theme.tertiary
                        }

                        StyledText {
                            anchors.verticalCenter: parent.verticalCenter
                            text: toolCard.summary
                            font.pixelSize: Theme.fontSizeSmall
                            font.weight: Theme.fontWeightMedium
                            color: Theme.surfaceText
                        }

                        StyledText {
                            anchors.verticalCenter: parent.verticalCenter
                            text: toolCard.anyRunning
                                ? I18n.tr("running…", "tool card status while the tool is executing")
                                : I18n.tr("done", "tool card status after the tool finished")
                            font.pixelSize: Theme.fontSizeSmall
                            color: Theme.surfaceVariantText
                        }

                        StyledText {
                            anchors.verticalCenter: parent.verticalCenter
                            text: root.toolExpanded ? "expand_less" : "expand_more"
                            font.family: Fonts.icons
                            font.pixelSize: Theme.iconSizeSmall
                            color: Theme.surfaceVariantText
                        }
                    }

                    MouseArea {
                        anchors.fill: parent
                        cursorShape: Qt.PointingHandCursor
                        onClicked: root.toggleTools()
                    }

                    // Same as the reasoning block: must sit above the MouseArea to receive clicks.
                    CopyButton {
                        visible: toolHover.hovered
                        anchors.right: parent.right
                        anchors.rightMargin: Theme.spacingXS
                        anchors.verticalCenter: parent.verticalCenter
                        copyText: root.toolPlainText()
                        Accessible.name: I18n.tr("Copy the tool output", "message action: copy the tool card")
                    }
                }

                // Body: one "command + output" block per call, monospace.
                Column {
                    width: parent.width
                    spacing: Theme.spacingM
                    visible: root.toolExpanded

                    Repeater {
                        model: toolCard.items.length
                        delegate: Column {
                            width: inner.width
                            spacing: Theme.spacingXS

                            Row {
                                spacing: Theme.spacingS

                                StyledText {
                                    text: toolCard.items[index].name || ""
                                    font.pixelSize: Theme.fontSizeSmall
                                    font.weight: Theme.fontWeightMedium
                                    color: Theme.surfaceText
                                    isMonospace: true
                                }

                                StyledText {
                                    text: toolCard.items[index].running === true
                                        ? I18n.tr("running…", "tool card status while the tool is executing")
                                        : ""
                                    font.pixelSize: Theme.fontSizeSmall
                                    color: Theme.surfaceVariantText
                                }
                            }

                            // What the model actually called.
                            StyledText {
                                width: parent.width
                                visible: text !== ""
                                text: TextFmt.expandTabs(root.toolCommand(toolCard.items[index].args || ""))
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.primary
                                isMonospace: true
                                wrapMode: Text.WrapAnywhere
                                maximumLineCount: 6
                                elide: Text.ElideRight
                            }

                            // Output: monospace, long lines unwrapped so structure stays visible.
                            StyledText {
                                width: parent.width
                                visible: (toolCard.items[index].output || "").trim() !== ""
                                // Tool output often contains tabs (gofmt, git diff); expanding them avoids Qt's 80 px tab stops.
                                text: TextFmt.expandTabs((toolCard.items[index].output || "").trim())
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceVariantText
                                isMonospace: true
                                wrapMode: Text.WrapAnywhere
                                maximumLineCount: 24
                                elide: Text.ElideRight
                            }
                        }
                    }
                }

                // The way back to a question the user put aside.
                //
                // Deliberately outside the body above: that one follows toolExpanded, and a card
                // folded shut (or the "closed" display setting) would hide the only way back to a
                // dialog the daemon is still waiting on.
                Row {
                    width: parent.width
                    spacing: Theme.spacingS
                    visible: root.questionWaiting

                    DButton {
                        text: I18n.tr("Show the question", "tool card button that reopens a model question the user hid")
                        iconName: "help"
                        variant: "tonal"
                        buttonHeight: Theme.buttonHeightXS
                        onClicked: Chat.restoreQuestion()
                    }

                    StyledText {
                        anchors.verticalCenter: parent.verticalCenter
                        text: I18n.tr("waiting for your answer", "tool card note beside the button that reopens a hidden question")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                    }
                }
            }
        }

        // ---------- user ----------
        Item {
            id: userWrap
            visible: root.isUser
            width: parent.width
            height: bubble.height + userActionSlot.height

            Item {
                id: userActionSlot

                anchors.top: bubble.bottom
                anchors.right: parent.right
                width: parent.width
                height: root.rawText.length > 0 ? 28 : 0

                Row {
                    anchors.right: parent.right
                    anchors.verticalCenter: parent.verticalCenter
                    spacing: Theme.spacingXS
                    opacity: hover.hovered ? 1 : 0
                    enabled: hover.hovered

                    Behavior on opacity {
                        enabled: Theme.animationsEnabled

                        NumberAnimation {
                            duration: Theme.expressiveDurations.expressiveEffects
                            easing.type: Easing.BezierSpline
                            easing.bezierCurve: Theme.expressiveCurves.standard
                        }
                    }

                    ActionButton {
                        iconName: "edit"
                        Accessible.name: I18n.tr("Edit", "message action: edit this prompt")
                        enabled: !Chat.busy
                        onActivated: root.editRequested(index, root.rawText)
                    }

                    ActionButton {
                        iconName: "content_copy"
                        Accessible.name: I18n.tr("Copy", "message action: copy this prompt")
                        onActivated: {
                            Quickshell.clipboardText = root.rawText;
                            ToastService.info(I18n.tr("Copied", "toast after copying a prompt"));
                        }
                    }
                }
            }

            TextMetrics {
                id: metrics
                font.family: Theme.fontFamily
                font.pixelSize: Theme.fontSizeMedium
                text: root.rawText
            }

            TextMetrics {
                id: attachMetrics
                font.family: Theme.fontFamily
                font.pixelSize: Theme.fontSizeSmall
                text: root.attachmentLabel
            }

            Rectangle {
                id: bubble
                anchors.right: parent.right
                width: Math.min(Math.max(metrics.advanceWidth, attachMetrics.advanceWidth) + Theme.spacingL * 2 + 4, parent.width * 0.82)
                height: userText.implicitHeight + Theme.spacingM * 2 + (attachLine.visible ? attachLine.implicitHeight + Theme.spacingXS : 0)
                topLeftRadius: Theme.cornerRadiusL
                topRightRadius: Theme.cornerRadiusL
                bottomLeftRadius: Theme.cornerRadiusL
                bottomRightRadius: Theme.cornerRadiusXS
                color: Theme.secondaryContainer

                Text {
                    id: userText
                    x: Theme.spacingL
                    y: Theme.spacingM
                    width: bubble.width - Theme.spacingL * 2
                    text: root.rawText
                    color: Theme.onSecondaryContainer
                    font.family: Theme.fontFamily
                    font.pixelSize: Theme.fontSizeMedium
                    font.weight: Theme.fontWeight
                    renderType: Text.NativeRendering
                    wrapMode: Text.Wrap
                    lineHeight: 1.45
                    visible: root.rawText !== ""
                }

                StyledText {
                    id: attachLine
                    x: Theme.spacingL
                    width: bubble.width - Theme.spacingL * 2
                    anchors.top: userText.bottom
                    anchors.topMargin: Theme.spacingXS
                    visible: root.attachmentList.length > 0
                    text: root.attachmentLabel
                    color: Theme.onSecondaryContainer
                    opacity: 0.75
                    font.pixelSize: Theme.fontSizeSmall
                    elide: Text.ElideMiddle
                    maximumLineCount: 1
                }
            }
        }

        // ---------- hover state ----------
        HoverHandler { id: hover }
    }

    // --- flash when arriving from a search hit ---
    // Jumping into the middle of a long thread leaves no clue which message matched. The frame
    // covers the column rather than the whole delegate, since the outer gutter would make it look
    // misplaced. MessageList clears it on a timer.
    Rectangle {
        anchors.fill: column
        anchors.margins: -Theme.spacingS
        radius: Theme.cornerRadiusL
        color: "transparent"
        border.width: Theme.focusRingWidth
        border.color: Theme.primary
        opacity: root.highlighted ? 1 : 0
        visible: opacity > 0

        Behavior on opacity {
            NumberAnimation {
                duration: 240
            }
        }
    }
}
