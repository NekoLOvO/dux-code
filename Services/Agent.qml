pragma Singleton
import QtQuick
import qs.Services
import qs.Common

// Agent — front-end proxy for one conversation turn.
//
// Execution happens in the Go daemon; this only sends chat.send and translates daemon
// events into signals. The public interface matches the old mock version (start/cancel
// plus started/delta/finished), so Chat.qml needed no change.
//
// Two run IDs: a local rid created by Chat to drop stale replies, and the daemon's
// runId. Events match on the latter and are translated back on the way out.
//
//
QtObject {
    id: agent

    readonly property var log: Log.scoped("Agent")

    property string localRunId: ""
    property string remoteRunId: ""
    property string sessionId: ""
    property bool waitingSend: false

    signal started(string rid)
    // The daemon persisted the user message of this run; carries its messageId so the
    // transcript row can address it before the session is reloaded.
    signal sent(string rid, string messageId)
    // Multi-step: the model is called again after each tool call, so the UI gets a new bubble.
    // previousMessageId is the persisted id of the message that bubble holds, or "" when unknown —
    // without it a bubble from an earlier step can never be addressed (edit, branch, truncate).
    signal stepStarted(string rid, int step, string previousMessageId)
    signal toolStarted(string rid, string callId, string name, string arguments)
    signal toolFinished(string rid, string callId, string output)
    signal delta(string rid, string text)
    // Persisted full reply (authoritative; used to reconcile streamed text).
    signal finalText(string rid, string text)
    // messageId is the id of the reply now on disk, or "" when nothing was persisted (a cancelled
    // run that wrote nothing). The transcript needs it: a live row carries no id, and every message
    // action is addressed by id.
    signal finished(string rid, bool cancelled, bool truncated, string messageId)
    // The record, not just the count: the transcript draws the seam at its cut point and can show the
    // summary itself, so both travel together.
    signal compacted(string sessionId, var record)
    // Folding has begun and will take a while; count is how many messages it covers.
    signal compacting(string sessionId, int count)
    signal reasoning(string rid, string text)
    signal finalReasoning(string rid, string text)
    // Extra metadata; Chat may ignore it.
    signal failed(string rid, string message)
    signal usage(string rid, int input, int output)
    // Everything the session has spent, as an absolute number computed by the daemon. Emitted on
    // every step and again on run.finished with the same meaning, so the receiver assigns it — it
    // must never add these up.
    signal sessionUsage(int total)

    function start(rid, prompt, attachments) {
        if (localRunId !== "")
            halt();

        localRunId = rid;
        remoteRunId = "";
        sessionId = Sessions.activeId;
        waitingSend = true;

        const params = {
            "sessionId": sessionId,
            "text": prompt
        };
        if (attachments && attachments.length > 0)
            params.attachments = attachments;
        const modelId = Models.currentId;
        if (modelId !== "")
            params.model = modelId;
        // Reasoning depth is sent only when explicitly chosen; "auto" omits it so the provider
        // keeps its default. The level string is ours — mapping it to provider parameters is
        // the daemon's job.
        const level = SettingsData.reasoningLevel(modelId);
        if (level !== "" && level !== "auto")
            params.reasoning = level;
        started(rid);

        Backend.sendRequest("chat.send", params, (res, err) => {
            waitingSend = false;
            if (localRunId !== rid)
                return; // This run was cancelled or replaced.
            if (err || !res) {
                log.warn("chat.send failed: " + err);
                fail(rid, err || I18n.tr("Failed to send", "toast when chat.send fails"));
                return;
            }
            // The event may arrive before the response; the run is claimed by then.
            if (remoteRunId === "")
                remoteRunId = res.runId || "";
            sent(rid, res.messageId || "");
        });
    }

    // Regenerate: no new user message; the daemon reruns the last one. Truncation
    // (session.truncate) is done by the caller.
    function regenerate(rid) {
        if (localRunId !== "")
            halt();

        localRunId = rid;
        remoteRunId = "";
        sessionId = Sessions.activeId;
        waitingSend = true;

        const params = {
            "sessionId": sessionId
        };
        const modelId = Models.currentId;
        if (modelId !== "")
            params.model = modelId;
        // Reasoning depth is sent only when explicitly chosen; "auto" omits it so the provider
        // keeps its default. The level string is ours — mapping it to provider parameters is
        // the daemon's job.
        const level = SettingsData.reasoningLevel(modelId);
        if (level !== "" && level !== "auto")
            params.reasoning = level;
        started(rid);

        Backend.sendRequest("chat.regenerate", params, (res, err) => {
            waitingSend = false;
            if (localRunId !== rid)
                return;
            if (err || !res) {
                log.warn("chat.regenerate failed: " + err);
                fail(rid, err || I18n.tr("Failed to regenerate", "toast when chat.regenerate fails"));
                return;
            }
            if (remoteRunId === "")
                remoteRunId = res.runId || "";
        });
    }

    function cancel() {        if (localRunId === "")
            return;
        const rid = localRunId;
        const gid = remoteRunId;
        // Invalidate the run first so any delta still in flight is dropped.
        localRunId = "";
        remoteRunId = "";
        halt();
        if (gid !== "")
            Backend.sendRequest("chat.cancel", { "runId": gid }, null);
        else if (sessionId !== "")
            // Adopted before any event arrived, so there is no runId to name. Cancelling by session
            // is what the daemon offers for exactly this: stop whatever is running here.
            Backend.sendRequest("chat.cancelSession", { "sessionId": sessionId }, null);
        // All four arguments, nothing persisted to hand over: a QML signal throws "TypeError:
        // Insufficient arguments" when called with fewer than it declares, and that would leave the
        // UI in "running" with the turn already stopped.
        finished(rid, true, false, "");
    }

    function halt() {
        waitingSend = false;
    }

    // Take over a turn this window is not running: the daemon was already mid-reply when the QML
    // reloaded, or the turn was started from the CLI.
    //
    // Without this the window keeps a composer that says "idle" while text arrives, and drops every
    // event at the localRunId guard below — the reply is still being produced, the window has just
    // stopped listening. The runId is unknown until the first event names it (the daemon only tells
    // us "busy"), so it is claimed from the stream like a self-started run whose response has not
    // arrived yet.
    //
    // Returns the local run id, or "" when there is already a turn here to adopt it into.
    function adopt(sessionID) {
        if (localRunId !== "" || sessionID === "")
            return "";
        localRunId = "run-adopted-" + Date.now();
        remoteRunId = "";
        sessionId = sessionID;
        waitingSend = false;
        verifyTimer.restart();
        return localRunId;
    }

    // Let go of a running turn without stopping it.
    //
    // The daemon owns every turn, not this window: leaving a session must not kill a reply the user is
    // waiting for. Switching away detaches — no chat.cancel — so the daemon keeps writing and the
    // transcript gets the rest; coming back adopts the turn again (see Chat._adoptRunningTurn).
    //
    // Stopping a turn is a separate decision, and it is the stop button: Agent.cancel().
    function detach() {
        if (localRunId === "")
            return false;
        verifyTimer.stop();
        localRunId = "";
        remoteRunId = "";
        sessionId = "";
        waitingSend = false;
        return true;
    }

    // A turn adopted from "this session is busy" has no runId of its own until an event names one.
    // If the turn ended in between — the reply finished in the instant between the daemon answering
    // and this window subscribing — nothing would ever arrive, and the composer would sit on a stop
    // button for a reply that is already over. One look afterwards settles it.
    readonly property Timer verifyTimer: Timer {
        interval: 1500
        onTriggered: {
            if (agent.localRunId === "" || agent.remoteRunId !== "")
                return;
            const rid = agent.localRunId;
            const sid = agent.sessionId;
            Backend.sendRequest("session.get", { "id": sid }, (res, err) => {
                if (err || !res || agent.localRunId !== rid)
                    return;
                if (res.busy === true)
                    return;
                agent.localRunId = "";
                agent.remoteRunId = "";
                agent.sessionId = "";
                // The session was just fetched, so the reply's id comes from it: this path never saw a
                // run.finished event, and without the id the row stays unaddressable.
                const list = (res.session && res.session.messages) || [];
                let messageId = "";
                for (let i = list.length - 1; i >= 0; i--) {
                    if ((list[i].role || "") === "assistant") {
                        messageId = list[i].id || "";
                        break;
                    }
                }
                agent.finished(rid, false, false, messageId);
            });
        }
    }

    function fail(rid, message) {
        localRunId = "";
        remoteRunId = "";
        halt();
        failed(rid, message);
        // Nothing was persisted, so there is no id to hand over; cancelled, because the row is not a
        // finished reply either.
        finished(rid, true, false, "");
    }

    // Claim the run: bind on the first matching runId and ignore the rest.
    function _claim(runId) {
        if (runId === "")
            return false;
        if (remoteRunId === "") {
            remoteRunId = runId;
            return true;
        }
        return remoteRunId === runId;
    }

    // The daemon sends the session's spend as {input, output}; the chip shows one number.
    function _reportSessionUsage(data) {
        const su = data ? data.sessionUsage : null;
        if (!su)
            return;
        sessionUsage((su.input || 0) + (su.output || 0));
    }

    readonly property Connections _link: Connections {
        target: Backend

        function onAgentEvent(data) {
            if (!data)
                return;
            // The session total also rides on events that arrive after the run closed (the title
            // request is billed then), so it is reported before the localRunId guard below.
            if (agent.sessionId !== "" && data.sessionId === agent.sessionId)
                agent._reportSessionUsage(data);

            // The daemon wrote config.json itself (a model's output ceiling learned from a 400). It belongs
            // to no run we started — a turn begun from the CLI learns the same fact — and the settings page
            // states that number, so it is handled before the localRunId guard.
            if (data.kind === "config.changed") {
                Backend.refreshConfig();
                return;
            }

            if (agent.localRunId === "")
                return;

            const rid = agent.localRunId;
            const kind = data.kind || "";
            const runId = data.runId || "";

            // An adopted turn knows only "this session is busy"; a scheduled task or a second window
            // can be running somewhere else at the same time. Events that name another session are
            // not ours. (Most of them carry no sessionId at all, which is why the runId claim below
            // is what binds a self-started turn.)
            if (data.sessionId !== undefined && data.sessionId !== "" && agent.sessionId !== "" && data.sessionId !== agent.sessionId)
                return;

            switch (kind) {
            case "run.started":
                agent._claim(runId);
                break;

            // Compaction happens before the first request of the turn, so localRunId exists.
            case "context.compacting":
                agent.compacting(data.sessionId || "", data.count || 0);
                break;

            case "context.compacted":
                agent.compacted(data.sessionId || "", data);
                break;

            // Thinking is a separate channel: it arrives first, may be long, and stays out of the answer.
            case "reasoning.delta":
                if (!agent._claim(runId))
                    return;
                agent.reasoning(rid, data.text || "");
                break;

            case "delta":
                if (!agent._claim(runId))
                    return;
                agent.delta(rid, data.text || "");
                break;

            case "run.step":
                if (rid !== "")
                    agent.stepStarted(rid, data.step || 0, data.previousMessageId || "");
                break;

            case "tool.started":
                if (rid !== "")
                    agent.toolStarted(rid, data.callId || "", data.name || "", data.arguments || "");
                break;

            case "tool.finished":
                if (rid !== "")
                    agent.toolFinished(rid, data.callId || "", data.output || "");
                break;

            // Per-step usage; run.finished repeats the same numbers, so this only keeps the
            // counter moving during the run.
            case "run.usage":
                if (!agent._claim(runId))
                    return;
                {
                    const u = data.lastUsage || data.usage;
                    if (u && (u.input > 0 || u.output > 0))
                        agent.usage(rid, u.input || 0, u.output || 0);
                }
                break;

            case "run.finished":
                if (!agent._claim(runId))
                    return;
                {
                    // Shows the last request's raw usage, not the run total.
                    const u = data.lastUsage || data.usage;
                    if (u && (u.input > 0 || u.output > 0))
                        agent.usage(rid, u.input || 0, u.output || 0);
                    // The daemon returns the persisted full text; use it to overwrite the stream.
                    if (data.message && typeof data.message.text === "string")
                        agent.finalText(rid, data.message.text);
                    // Persisted thinking text (fills in anything missed).
                    if (data.message && typeof data.message.reasoning === "string")
                        agent.finalReasoning(rid, data.message.reasoning);
                }
                agent.verifyTimer.stop();
                agent.localRunId = "";
                agent.remoteRunId = "";
                agent.halt();
                agent.finished(rid, (data.status || "") === "cancelled", data.truncated === true,
                               (data.message && data.message.id) || "");
                break;

            case "run.failed":
                if (!agent._claim(runId))
                    return;
                agent.fail(rid, data.error || I18n.tr("Unknown error", "toast fallback when the daemon sends no message"));
                break;
            }
        }
    }
}
