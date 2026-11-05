pragma Singleton
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Io
import qs.Common
import qs.Services
import qs.DCommon.Common

// Backend — the only seam between the UI and the Go daemon.
//
// Transport is NDJSON over a Unix socket, one JSON object per line:
//   handshake  {"apiVersion":1,"capabilities":[...]}
//   request    {"id":3,"method":"session.list","params":{}}
//   response   {"id":3,"result":{...}}
//   event      {"event":"agent","data":{...}}
//
// The rest of the UI only knows this singleton's properties and sendRequest().
Singleton {
    id: root

    readonly property var log: Log.scoped("Backend")

    // --- Transport ---
    readonly property string socketPath: {
        const override = Quickshell.env("DUXCODE_SOCKET");
        if (override && override.length > 0)
            return override;
        const runtime = Quickshell.env("XDG_RUNTIME_DIR");
        return (runtime && runtime.length > 0 ? runtime : "/tmp") + "/dcode/daemon.sock";
    }

    // connected means "daemon usable": true only once the handshake carries an
    // apiVersion, so a socket that has not spoken yet stays false.
    readonly property bool connected: _linkUp
    readonly property bool subscribed: _subscribed
    readonly property var capabilities: _capabilities
    readonly property string daemonVersion: _version
    readonly property int apiVersion: _apiVersion

    property bool _linkUp: false
    property bool _subscribed: false
    property var _capabilities: []
    property string _version: ""
    property int _apiVersion: 0
    property string lastError: ""

    // --- daemon description ---
    property var models: []
    property string defaultModel: ""
    property string workingDirectory: ""
    // For the Providers page: key source and hasKey only, never the key itself.
    property var providers: []
    property var providerTemplates: []
    property string configPath: ""
    // Hard cap on output tokens per reply; 0 means off, which is the default. The
    // agent system prompt states the number actually enforced, because exceeding it
    // truncates the answer. A model that reports its own ceiling lowers this value
    // on the way out — see the daemon's OutputCap.
    property int maxOutputTokens: 0
    // Maximum model calls per turn; one tool round trip counts as a step. This is a
    // loop guard, not a workload limit. Real sessions have been measured at 116
    // steps in a single turn, so the default sits well above normal use.
    property int maxToolSteps: 200
    // Compaction threshold in estimated tokens, 0 disables it. Estimates the history
    // about to be sent, which is unrelated to the billed token total. Each request
    // re-sends that history, so raising this raises what a turn costs.
    property int compactAt: 0
    // The same threshold as the daemon will actually apply to the current model: lower
    // than compactAt when the setting is above what the model can read. 0 means off.
    property int effectiveCompactAt: 0
    // Tool approval policy, enforced by the daemon so it lives in config.json:
    // "ask" every time, "workspace" only auto-approves actions that stay inside the
    // working directory, "auto" everything.
    property string approvalMode: "ask"
    // Whether bwrap exists on this machine. Without it, "inside the workspace"
    // degrades to literal path checks, so the settings page has to say so.
    property bool sandboxAvailable: true

    // Disabled tools are removed from the model's tool list, not just refused when
    // called — otherwise the model keeps retrying them.
    property bool fileAccess: true
    property bool shellAccess: true
    // The web-search toggle in the composer; the capability itself is backend-side.
    property bool webSearch: true

    // Browser tool location. browserResolved is the binary the daemon will actually
    // use; when it is empty, browserReason explains why. No separate switch: it
    // shares webSearch with search and page fetch.
    property string browserPath: ""
    property string browserResolved: ""
    property string browserReason: ""

    // Search backends. Never carries keys: the daemon only reports hasKey.
    property var searchProviders: []
    // MCP servers: launch spec and runtime status; env exposes key names only.
    property var mcpServers: []
    property var searchKinds: []
    property string searchDefault: ""
    readonly property var ready: models.length > 0

    // --- request correlation ---
    property var pendingRequests: ({})
    property int requestCounter: 0

    // --- Reverse requests (daemon asks the UI) ---
    // One kind today: a tool that touches files or runs commands needs approval.
    //
    // A state machine, not a callback: a request can be answered, dismissed or time
    // out, and the app may be closed while it waits. The daemon times out after five
    // minutes, but the UI must answer whatever it receives or the tool stays blocked.
    property var permissionRequests: []
    // Questions from the model: also a reverse request, but with no timeout — waiting
    // on a person should not be killed after five minutes.
    property var questions: []

    signal connectionStateChanged
    signal colorSchemeUpdate(var data)
    // Raw agent events; Sessions and Chat interpret them.
    signal agentEvent(var data)
    // Emitted when a scheduled task finishes (daemon tasks.Manager broadcast). Kept
    // apart from agent events: it is task-level, not a conversation stream.
    signal taskEvent(var data)
    signal modelsRefreshed
    // The session list needs a reload (just connected, or a session.* call changed it).
    signal sessionsStale
    // A permission request is pending; the UI opens a dialog.
    signal permissionPending
    // The model asked a question; the UI opens a prompt.
    signal questionPending

    // --- connection ---
    function _connect() {
        socket.connected = true;
    }

    function _handleLine(line) {
        if (!line || line.length === 0)
            return;

        var msg;
        try {
            msg = JSON.parse(line);
        } catch (e) {
            root.log.warn("non-JSON line: " + line.slice(0, 80));
            return;
        }

        // Handshake: the only line carrying apiVersion.
        if (msg.apiVersion !== undefined) {
            _apiVersion = msg.apiVersion || 0;
            _version = msg.version || "";
            _capabilities = msg.capabilities || [];
            _linkUp = true;
            root.log.info("daemon connected (api v" + _apiVersion + ("" !== _version ? ", " + _version : "") + ")");
            subscribeTopics(["agent", "task"]);
            _subscribed = true;
            refreshInfo();
            refreshSessions();
            connectionStateChanged();
            return;
        }

        // Reverse request: the daemon asks the UI something and expects an answer.
        // It uses rid instead of id so it cannot collide with responses.
        if (msg.rid !== undefined) {
            _handleReverse(msg);
            return;
        }

        // Event
        if (msg.event !== undefined) {
            if (msg.event === "agent") {
                agentEvent(msg.data || ({}));
            } else if (msg.event === "task") {
                taskEvent(msg.data || ({}));
            } else if (msg.event === "colorScheme") {
                colorSchemeUpdate(msg.data || ({}));
            }
            return;
        }

        // Response
        if (msg.id === undefined)
            return;
        var cb = pendingRequests[msg.id];
        if (!cb)
            return;
        var next = Object.assign({}, pendingRequests);
        delete next[msg.id];
        pendingRequests = next;
        if (msg.error)
            cb(null, msg.error);
        else
            cb(msg.result, "");
    }

    function _handleLinkChanged() {
        if (socket.linkUp) {
            // Really usable only after the handshake, see _handleLine
            return;
        }
        if (_linkUp) {
            _linkUp = false;
            _subscribed = false;
            _capabilities = [];
            _clearPermissions();
            root.log.warn("lost the daemon connection, reconnecting");
            connectionStateChanged();
        }
    }

    // --- reverse requests ---
    function _handleReverse(msg) {
        if (msg.method === "permission.request") {
            const p = msg.params || ({});
            permissionRequests = permissionRequests.concat([{
                "rid": msg.rid,
                "kind": p.kind || "",
                "title": p.title || "",
                "detail": p.detail || "",
                "paths": p.paths || []
            }]);
            root.log.info("permission request: " + (p.title || p.kind || ""));
            permissionPending();
            return;
        }

        if (msg.method === "question.request") {
            const q = msg.params || ({});
            questions = questions.concat([{
                "rid": msg.rid,
                "question": q.question || "",
                "options": q.options || []
            }]);
            root.log.info("question: " + (q.question || ""));
            questionPending();
            return;
        }

        // Unknown methods still get a reply, otherwise the daemon waits for its timeout.
        socket.send({
            "rid": msg.rid,
            "error": "UI does not support this request: " + msg.method
        });
    }

    // allow must be a boolean; reason only matters on denial (it is shown to the model).
    function answerPermission(rid, allow, reason) {
        socket.send({
            "rid": rid,
            "result": {
                "allow": allow === true,
                "reason": String(reason || "")
            }
        });
        permissionRequests = permissionRequests.filter(r => r.rid !== rid);
    }

    // An empty answer means "declined to answer": the daemon tells the model to pick a
    // path on its own instead of waiting.
    function answerQuestion(rid, answer) {
        socket.send({
            "rid": rid,
            "result": {
                "answer": String(answer || "")
            }
        });
        questions = questions.filter(q => q.rid !== rid);
    }

    // Losing the connection voids every pending request (the daemon gets ErrDisconnected).
    function _clearPermissions() {
        if (permissionRequests.length > 0)
            permissionRequests = [];
        if (questions.length > 0)
            questions = [];
    }

    // --- requests ---
    function sendRequest(method, params, callback) {
        if (!_linkUp) {
            if (callback)
                callback(null, "daemon not connected");
            return -1;
        }
        requestCounter += 1;
        const id = requestCounter;
        if (callback) {
            const next = Object.assign({}, pendingRequests);
            next[id] = callback;
            pendingRequests = next;
        }
        socket.send({
            "id": id,
            "method": method,
            "params": params || ({})
        });
        return id;
    }

    function subscribeTopics(topics) {
        if (!_linkUp || !topics || topics.length === 0)
            return;
        socket.send({
            "id": 0,
            "method": "subscribe",
            "params": {
                "topics": topics
            }
        });
    }

    function unsubscribeTopics(topics) {
        if (!_linkUp || !topics || topics.length === 0)
            return;
        socket.send({
            "id": 0,
            "method": "unsubscribe",
            "params": {
                "topics": topics
            }
        });
    }

    // --- cache ---
    function _applyConfig(res) {
        if (!res)
            return;
        if (res.models)
            models = res.models;
        if (res.defaultModel !== undefined)
            defaultModel = res.defaultModel;
        if (res.workingDirectory !== undefined)
            workingDirectory = res.workingDirectory;
        if (res.providers)
            providers = res.providers;
        if (res.configPath !== undefined)
            configPath = res.configPath;
        // 0 is a real value now ("off"), so only an absent field is ignored.
        if (res.maxOutputTokens !== undefined)
            maxOutputTokens = res.maxOutputTokens;
        if (res.compactAt !== undefined)
            compactAt = res.compactAt;
        if (res.effectiveCompactAt !== undefined)
            effectiveCompactAt = res.effectiveCompactAt;
        if (res.maxToolSteps !== undefined)
            maxToolSteps = res.maxToolSteps;
        if (res.approvalMode !== undefined && res.approvalMode !== "")
            approvalMode = res.approvalMode;
        if (res.sandbox !== undefined)
            sandboxAvailable = res.sandbox;
        if (res.autostart !== undefined)
            autostartEnabled = res.autostart === true;
        if (res.tools !== undefined) {
            fileAccess = res.tools.fileAccess !== false;
            shellAccess = res.tools.shell !== false;
            webSearch = res.tools.webSearch !== false;
        }
        if (res.browser !== undefined) {
            browserPath = res.browser.path || "";
            browserResolved = res.browser.resolved || "";
            browserReason = res.browser.reason || "";
        }
        if (res.search !== undefined) {
            searchProviders = res.search.providers || [];
            searchDefault = res.search.default || "";
        }
        if (res.searchKinds !== undefined)
            searchKinds = res.searchKinds;
        if (res.mcp !== undefined)
            mcpServers = res.mcp.servers || [];
        modelsRefreshed();
    }

    function refreshInfo() {
        sendRequest("info", {}, (res, err) => {
            if (!res || err) {
                root.log.warn("info failed: " + err);
                return;
            }
            _applyConfig(res);
        });
        // info omits providers and templates; fetch them separately.
        refreshConfig();
        refreshTemplates();
    }

    function refreshTemplates() {
        sendRequest("provider.templates", {}, (res, err) => {
            if (!res || err)
                return;
            providerTemplates = res.templates || [];
        });
    }

    function refreshConfig() {
        sendRequest("config.get", {}, (res, err) => {
            if (!res || err) {
                root.log.warn("config.get failed: " + err);
                return;
            }
            _applyConfig(res);
        });
    }

    // Writes an API key. An empty string clears it and reads the environment variable again.
    function setProviderKey(name, key, callback) {
        sendRequest("config.setKey", {
            "provider": name,
            "apiKey": String(key === undefined || key === null ? "" : key)
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function setCompactAt(value, callback) {
        sendRequest("config.setAgent", {
            "compactAt": value
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    // Syntax highlighting: the daemon runs chroma and returns HTML with inline styles,
    // which TextEdit renders as rich text without building spans in QML. "light" is
    // passed in because it is UI theme state and the daemon should not guess it.
    function highlightCode(lang, code, light, callback) {
        sendRequest("code.highlight", {
            "lang": lang || "",
            "code": code || "",
            "light": light === true
        }, (res, err) => {
            if (callback)
                callback(res ? (res.html || "") : "", err);
        });
    }

    function mcpServerById(id) {
        for (var i = 0; i < mcpServers.length; i++) {
            if (mcpServers[i].id === id)
                return mcpServers[i];
        }
        return null;
    }

    // Saving makes the daemon start or stop processes right away, and the returned
    // config already carries the new state.
    function saveMCPServer(params, callback) {
        sendRequest("mcp.save", params, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function removeMCPServer(id, callback) {
        sendRequest("mcp.remove", {
            "id": id
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function toggleMCPServer(id, enabled, callback) {
        sendRequest("mcp.toggle", {
            "id": id,
            "enabled": enabled === true
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    // Restart means try again. A server that failed to start is not retried automatically.
    function restartMCPServer(id, callback) {
        sendRequest("mcp.restart", {
            "id": id
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function searchProviderById(id) {
        for (var i = 0; i < searchProviders.length; i++) {
            if (searchProviders[i].id === id)
                return searchProviders[i];
        }
        return null;
    }

    function setFileAccess(enabled, callback) {
        sendRequest("config.setAgent", {
            "fileAccess": enabled === true
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function setWebSearch(enabled, callback) {
        sendRequest("config.setAgent", {
            "webSearch": enabled === true
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    // Empty string is valid: it means "resolve from PATH", not "clear the setting".
    function setBrowserPath(path, callback) {
        sendRequest("config.setAgent", {
            "browserPath": path || ""
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function setShellAccess(enabled, callback) {
        sendRequest("config.setAgent", {
            "shell": enabled === true
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    // Create or update a search backend. Without an id it is created; on edit an empty
    // apiKey keeps the stored one.
    function saveSearchProvider(params, callback) {
        sendRequest("search.save", params, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function removeSearchProvider(id, callback) {
        sendRequest("search.remove", {
            "id": id
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    // Make one backend the active one.
    function useSearchProvider(id, callback) {
        sendRequest("search.use", {
            "id": id
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    // Runs a real query rather than a ping. Returns {count, results, elapsedMs}.
    function testSearch(params, callback) {
        sendRequest("search.test", params, (res, err) => {
            if (callback)
                callback(res, err);
        });
    }

    function setApprovalMode(value, callback) {
        sendRequest("config.setAgent", {
            "approvalMode": value
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function setMaxOutputTokens(value, callback) {
        sendRequest("config.setAgent", {
            "maxOutputTokens": value
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function setMaxToolSteps(value, callback) {
        sendRequest("config.setAgent", {
            "maxToolSteps": value
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function setDefaultModel(id, callback) {
        sendRequest("config.setDefaultModel", {
            "id": id
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    // --- provider management ---
    // Every write returns the full config and replaces the local cache, so the UI never
    // guesses what the daemon changed.

    function _afterMutation(res, err, callback) {
        if (res && res.config)
            _applyConfig(res.config);
        else if (res)
            _applyConfig(res);
        if (callback)
            callback(res, err);
    }

    // Probes the endpoint without persisting; the add flow uses it to list models.
    function probeProvider(type, baseUrl, apiKey, callback) {
        sendRequest("provider.probe", {
            "type": type,
            "baseUrl": baseUrl,
            "apiKey": apiKey
        }, callback);
    }

    function addProvider(params, callback) {
        sendRequest("provider.add", params, (res, err) => {
            _afterMutation(res, err, callback);
        });
    }

    // Updates only the fields passed in. An empty apiKey keeps the stored key; pass
    // clearApiKey: true to remove it.
    function updateProvider(id, fields, callback) {
        const params = Object.assign({
            "id": id
        }, fields);
        sendRequest("provider.update", params, (res, err) => {
            _afterMutation(res, err, callback);
        });
    }

    function removeProvider(id, callback) {
        sendRequest("provider.remove", {
            "id": id
        }, (res, err) => {
            _afterMutation(res, err, callback);
        });
    }

    function fetchProviderModels(id, callback) {
        sendRequest("provider.fetchModels", {
            "id": id
        }, (res, err) => {
            if (res)
                _applyConfig(res.config);
            if (callback)
                callback(res, err);
        });
    }

    function setProviderModel(id, model, enabled, callback) {
        sendRequest("provider.setModel", {
            "id": id,
            "model": model,
            "enabled": enabled
        }, (res, err) => {
            if (res)
                _applyConfig(res);
            if (callback)
                callback(res, err);
        });
    }

    function providerById(id) {
        for (let i = 0; i < providers.length; i++) {
            if (providers[i].id === id)
                return providers[i];
        }
        return null;
    }

    function refreshSessions() {
        sessionsStale();
    }

    // --- legacy interface still used by the settings page and FilesService ---
    property bool autostartEnabled: false
    readonly property var accounts: []
    readonly property var calendars: []

    function accountLabel(account) {
        return account && account.name ? account.name : "";
    }
    function accountFlavor(account) {
        return "";
    }
    function refreshAccount(account) {
    }
    function reconnectAccount(account) {
    }
    function removeAccount(account) {
    }
    function writableCalendars() {
        return [];
    }
    function eventCalendars() {
        return [];
    }
    function eventCalendarGroups() {
        return [];
    }
    function hasTaskLists() {
        return false;
    }
    function setCalendarHidden(id, hidden) {
    }
    function setCalendarSyncDisabled(id, disabled) {
    }
    function deleteCalendar(id) {
    }
    function renameCalendar(id, name) {
    }
    function sendTestReminder() {
    }
    // Autostart: the daemon writes or removes ~/.config/autostart/<app>.desktop.
    // The switch flips immediately and reverts if the call fails.
    function setAutostart(enabled) {
        const want = enabled === true;
        if (autostartEnabled === want)
            return;
        autostartEnabled = want;
        sendRequest("config.setAutostart", {
            "enabled": want
        }, (res, err) => {
            if (res) {
                _applyConfig(res);
                return;
            }
            autostartEnabled = !want;
            root.log.warn("config.setAutostart failed: " + err);
        });
    }
    function restart() {
    }
    function quit() {
    }

    Component.onCompleted: _connect()

    DSocket {
        id: socket
        path: root.socketPath
        parser: SplitParser {
            onRead: line => root._handleLine(line)
        }
        onConnectionStateChanged: root._handleLinkChanged()
    }
}
