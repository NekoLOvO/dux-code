import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.DCommon.Widgets

// Add or edit an MCP server.
//
// One page, with the kind of server (local command or remote URL) picked up front: the two need
// different fields, and only after that choice is it clear where a token can go.
//
// The token is write-only in both directions. The daemon reports the key names of env / headers and
// never the values, so the field starts empty every time and an empty field means "keep what is
// stored" (see mergeSecrets in the daemon). Typing a token fills a header for a remote server and an
// environment variable for a local one; "More options" is the same map row by row, for anything the
// one-field shortcut cannot express.
FloatingWindow {
    id: modal

    property bool editing: false
    property string editId: ""
    property bool busy: false
    property string errorText: ""

    property string label: ""
    // Which way this server is reached, so the token field below knows whether it is filling an
    // environment variable or a header.
    property string mode: "local"
    // A non-empty url means remote (streamable HTTP); command is then unused.
    property string url: ""
    property string command: ""
    property string argsText: ""
    property bool autoApprove: false
    property bool enabled: true
    // The token, in the clear only until the next save: nothing reads it back out of the daemon, so
    // an empty field means "keep the stored one" and there is no way to display it.
    property string token: ""
    // Where a local server's token goes. Server-specific (GitHub's server reads
    // GITHUB_PERSONAL_ACCESS_TOKEN), so it cannot be a constant. Empty is not a default to guess at:
    // a token typed without a name is refused, because a variable under the wrong name is a server
    // that starts and then behaves as if it had no token at all.
    property string tokenName: ""
    // The full map, editable row by row under "More options". Values are empty for a stored key and stay
    // empty unless the user types a new one — the daemon keeps what it has (see mergeSecrets).
    property var envRows: []
    property var headerRows: []
    property bool advanced: false
    // Env var / header names already configured, shown when editing (keys only, never values).
    property var envKeys: []
    property var headerKeys: []

    // No default: a FloatingWindow with no parentWindow is a plain xdg_toplevel, and a tiling
    // compositor (Hyprland, niri, sway) gives those a tile — the modal then opens as a full-height
    // column instead of floating over the window that asked for it. Quickshell cannot guess which
    // window a modal belongs to, so every instantiation site names one. `make check` enforces it.

    function reset() {
        editing = false;
        editId = "";
        busy = false;
        errorText = "";
        label = "";
        mode = "local";
        url = "";
        command = "";
        argsText = "";
        autoApprove = false;
        enabled = true;
        token = "";
        tokenName = "";
        envRows = [];
        headerRows = [];
        advanced = false;
        envKeys = [];
        headerKeys = [];
    }

    // A stored key shows up as a row with no value: the name is what the user needs to see (and to
    // delete), the value never leaves the daemon.
    function rowsFromNames(names) {
        return (names || []).map(n => ({ "name": n, "value": "" }));
    }

    readonly property var rows: mode === "remote" ? headerRows : envRows
    readonly property var storedKeys: mode === "remote" ? headerKeys : envKeys

    function rowName(index, name) {
        rows[index].name = name;
    }

    function rowValue(index, value) {
        rows[index].value = value;
    }

    function addRow() {
        if (mode === "remote")
            headerRows = headerRows.concat([{ "name": "", "value": "" }]);
        else
            envRows = envRows.concat([{ "name": "", "value": "" }]);
    }

    function removeRow(index) {
        if (mode === "remote")
            headerRows = headerRows.filter((r, i) => i !== index);
        else
            envRows = envRows.filter((r, i) => i !== index);
    }

    // Blank names are how a half-typed row looks; they are not keys.
    function mapOf(list) {
        const out = {};
        for (let i = 0; i < list.length; i++) {
            const key = String(list[i].name || "").trim();
            if (key !== "")
                out[key] = String(list[i].value || "");
        }
        return out;
    }

    function show() {
        reset();
        visible = true;
    }

    function showFor(id) {
        reset();
        const s = Backend.mcpServerById(id);
        if (!s)
            return;
        editing = true;
        editId = id;
        label = s.label || "";
        mode = (s.url || "") !== "" ? "remote" : "local";
        url = s.url || "";
        command = s.command || "";
        argsText = joinArgs(s.args);
        autoApprove = s.autoApprove === true;
        enabled = s.disabled !== true;
        envKeys = s.envKeys || [];
        headerKeys = s.headerKeys || [];
        envRows = rowsFromNames(envKeys);
        headerRows = rowsFromNames(headerKeys);
        // One stored variable is almost always the token this server reads, so name it: pasting a new
        // token then replaces that variable instead of adding a second one next to it. More than one
        // and there is nothing to guess from.
        tokenName = envKeys.length === 1 ? envKeys[0] : "";
        visible = true;
    }

    function hide() {
        visible = false;
    }

    // Arguments are space-separated; quote anything containing spaces. Settings always store
    // an array — this string is only the field's representation.
    function splitArgs(text) {
        const out = [];
        let cur = "";
        let quoted = false;
        for (let i = 0; i < text.length; i++) {
            const ch = text[i];
            if (ch === "\\" && i + 1 < text.length && text[i + 1] === '"') {
                cur += '"';
                i++;
                continue;
            }
            if (ch === '"') {
                quoted = !quoted;
                continue;
            }
            if (!quoted && (ch === " " || ch === "\t")) {
                if (cur !== "") {
                    out.push(cur);
                    cur = "";
                }
                continue;
            }
            cur += ch;
        }
        if (cur !== "")
            out.push(cur);
        return out;
    }

    function joinArgs(list) {
        if (!list)
            return "";
        return list.map(a => /[\s"]/.test(a) ? '"' + String(a).replace(/"/g, '\\"') + '"' : a).join(" ");
    }

    function save() {
        if (busy)
            return;
        const isRemote = mode === "remote";
        const remote = isRemote ? String(url).trim() : "";
        const cmd = isRemote ? "" : String(command).trim();
        if (remote === "" && cmd === "") {
            errorText = isRemote
                ? I18n.tr("Enter the server's URL.", "mcp form error when the url is empty")
                : I18n.tr("Enter the command to run.", "mcp form error when the command is empty");
            return;
        }
        const typed = String(token).trim();
        const tokenVar = String(tokenName).trim();
        if (typed !== "" && !isRemote && tokenVar === "") {
            // Guessing a name would start the server and then leave it behaving as if it had no token
            // at all, which is worse than saying what is missing.
            errorText = I18n.tr("Name the environment variable the token belongs in.", "mcp form error when a local token has no variable name");
            return;
        }
        busy = true;
        errorText = "";

        // The token field is a shortcut into the map below it: a header for a remote server, an
        // environment variable for a local one. Typing nothing leaves whatever is stored alone.
        const env = mapOf(envRows);
        const headers = mapOf(headerRows);
        if (typed !== "") {
            if (isRemote) {
                // A bare token becomes a bearer header; one that already looks like "Scheme value" is
                // stored as typed, so other schemes stay reachable without an extra field.
                headers["Authorization"] = /\s/.test(typed) ? typed : "Bearer " + typed;
            } else {
                env[tokenVar] = typed;
            }
        }

        const params = {
            "label": String(label).trim(),
            "url": remote,
            "command": cmd,
            "args": isRemote ? [] : splitArgs(argsText),
            "env": env,
            "headers": headers,
            "disabled": !enabled,
            "autoApprove": autoApprove
        };
        if (editing)
            params.id = editId;

        Backend.saveMCPServer(params, (res, err) => {
            busy = false;
            if (err) {
                errorText = err;
                return;
            }
            ToastService.info(editing ? I18n.tr("Saved", "toast after saving an mcp server") : I18n.tr("MCP server added", "toast after adding an mcp server"));
            hide();
        });
    }

    title: editing ? I18n.tr("Edit MCP server", "mcp modal window title when editing") : I18n.tr("Add MCP server", "mcp modal window title")
    minimumSize: Qt.size(520, 460)
    implicitWidth: Math.max(minimumSize.width, Theme.modalWidth(parentWindow, screen, 600))
    implicitHeight: Math.max(minimumSize.height, Math.min(screen ? screen.height - 80 : 900, 560))
    // Alpha-aware: this is what the opacity setting changes.
    color: Theme.windowSurface
    surfaceFormat.opaque: false

    // Asks the compositor to blur what shows through, when the window is see-through and the user
    // asked for it. On a compositor without the protocol this is a silent no-op rather than an error,
    // which is why the setting says so instead of being gated on a probe.
    WindowBlur {
        targetWindow: modal
        blurEnabled: Theme.translucentWindows
        blurWidth: modal.width
        blurHeight: modal.height
    }
    visible: false

    // The compositor can close this window without going through hide() (Hyprland's super+q, a
    // window manager's close button), and that only emits closed — visible stays true. The window is
    // then unmapped but still "visible" as far as QML knows, so it reappears the next time anything
    // makes it show again: closing it, closing settings, and reopening settings brought both back.
    // SettingsModal has done this all along; every window here needs it.
    onClosed: hide()

    Column {
        anchors.fill: parent
        spacing: 0

        LayoutMirroring.enabled: I18n.isRtl
        LayoutMirroring.childrenInherit: true

        Item {
            id: headerRow

            width: parent.width
            height: header.height
            z: 10

            DWindowHeader {
                id: header

                anchors.left: parent.left
                anchors.right: parent.right
                controls: windowControls
                title: modal.title
                onCloseRequested: modal.hide()
            }
        }

        Item {
            width: parent.width
            height: parent.height - headerRow.height
            clip: true

            DFlickable {
                anchors.fill: parent
                anchors.margins: Theme.spacingL
                clip: true
                contentWidth: width
                contentHeight: formColumn.implicitHeight

                Column {
                    id: formColumn

                    width: parent.width
                    spacing: Theme.spacingM

                    StyledText {
                        text: I18n.tr("An MCP server hands the model extra tools. Run one on this machine, or connect to a server someone else hosts. A local server runs as you do, outside the workspace sandbox.", "mcp form intro")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    // Local or remote is a choice, not something to infer from whichever field has
                    // text in it. The two need different fields, and only one of them can carry a
                    // token — which is the part that made this form hard to finish.
                    Column {
                        width: parent.width
                        spacing: Theme.spacingS

                        StyledText {
                            text: I18n.tr("Kind", "mcp form mode label")
                            font.pixelSize: Theme.fontSizeSmall
                            color: Theme.surfaceVariantText
                        }

                        DButtonGroup {
                            model: [
                                I18n.tr("Runs on this machine", "mcp form mode: a server launched as a local process"),
                                I18n.tr("Remote server", "mcp form mode: a server reached over HTTP")
                            ]
                            currentIndex: modal.mode === "remote" ? 1 : 0
                            size: "small"
                            onSelectionChanged: (index, selected) => {
                                if (selected)
                                    modal.mode = index === 1 ? "remote" : "local";
                            }
                        }
                    }

                    DTextField {
                        width: parent.width
                        outlined: true
                        labelText: I18n.tr("Name (optional)", "mcp form name field label")
                        placeholderText: I18n.tr("e.g. GitHub", "mcp form name placeholder")
                        text: modal.label
                        onTextChanged: modal.label = text
                        Component.onCompleted: forceActiveFocus()
                    }

                    // ---- remote ----
                    DTextField {
                        width: parent.width
                        outlined: true
                        visible: modal.mode === "remote"
                        labelText: I18n.tr("Server URL", "mcp form url field label")
                        placeholderText: "https://mcp.example.com/mcp"
                        leftIconName: "link"
                        text: modal.url
                        onTextChanged: modal.url = text
                    }

                    StyledText {
                        visible: modal.mode === "remote"
                        text: I18n.tr("Streamable HTTP, so nothing runs on this machine. A server that keeps its tools on the internet is reached this way.", "mcp form url hint")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    // ---- local ----
                    DTextField {
                        width: parent.width
                        outlined: true
                        visible: modal.mode === "local"
                        labelText: I18n.tr("Command", "mcp form command field label")
                        placeholderText: "npx"
                        leftIconName: "terminal"
                        text: modal.command
                        onTextChanged: modal.command = text
                    }

                    DTextField {
                        width: parent.width
                        outlined: true
                        visible: modal.mode === "local"
                        labelText: I18n.tr("Arguments", "mcp form args field label")
                        placeholderText: "-y @modelcontextprotocol/server-filesystem /home/you"
                        text: modal.argsText
                        onTextChanged: modal.argsText = text
                    }

                    StyledText {
                        visible: modal.mode === "local"
                        text: I18n.tr("Separate arguments with spaces. Wrap one in double quotes if it contains spaces.", "mcp form args hint")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    // ---- the token ----
                    //
                    // Write-only by construction: the daemon reports the key names and never the
                    // values, so this field starts empty every time and an empty field means "keep
                    // what is stored". That is the same rule the provider key field follows.
                    StyledText {
                        text: I18n.tr("Access token", "mcp form token section label")
                        font.pixelSize: Theme.fontSizeMedium
                        color: Theme.surfaceText
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    DTextField {
                        width: parent.width
                        outlined: true
                        visible: modal.mode === "local"
                        labelText: I18n.tr("Variable name", "mcp form env variable name label")
                        placeholderText: "GITHUB_PERSONAL_ACCESS_TOKEN"
                        text: modal.tokenName
                        onTextChanged: modal.tokenName = text
                    }

                    DTextField {
                        width: parent.width
                        outlined: true
                        labelText: modal.storedKeys.length > 0
                            ? I18n.tr("Token — leave empty to keep the saved one", "mcp form token label when a token is stored")
                            : I18n.tr("Token", "mcp form token field label")
                        placeholderText: modal.storedKeys.length > 0 ? "••••••••" : "ghp_…"
                        leftIconName: "key"
                        echoMode: TextInput.Password
                        showPasswordToggle: true
                        text: modal.token
                        onTextChanged: modal.token = text
                    }

                    StyledText {
                        text: modal.mode === "remote"
                            ? I18n.tr("Sent to the server as an Authorization header. Nothing else on this machine sees it.", "mcp form token hint for a remote server")
                            : I18n.tr("Given to the server as the environment variable you named — GitHub's server reads GITHUB_PERSONAL_ACCESS_TOKEN. It stays in config.json and is never shown again.", "mcp form token hint for a local server")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    StyledText {
                        visible: modal.storedKeys.length > 0
                        text: I18n.tr("Saved: %1", "mcp form list of stored secret names").arg(modal.storedKeys.join(", "))
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    // ---- advanced: the whole map ----
                    DButton {
                        text: modal.advanced
                            ? I18n.tr("Fewer options", "mcp form advanced section collapse")
                            : I18n.tr("More options", "mcp form advanced section expand")
                        variant: "text"
                        iconName: modal.advanced ? "expand_less" : "expand_more"
                        buttonHeight: Theme.buttonHeightXS
                        onClicked: modal.advanced = !modal.advanced
                    }

                    Column {
                        width: parent.width
                        spacing: Theme.spacingS
                        visible: modal.advanced

                        StyledText {
                            text: modal.mode === "remote"
                                ? I18n.tr("Headers", "mcp form advanced section label for a remote server")
                                : I18n.tr("Environment variables", "mcp form advanced section label for a local server")
                            font.pixelSize: Theme.fontSizeSmall
                            color: Theme.surfaceVariantText
                        }

                        Repeater {
                            model: modal.rows

                            delegate: Row {
                                id: secretRow

                                required property int index
                                required property var modelData

                                width: formColumn.width
                                spacing: Theme.spacingS

                                DTextField {
                                    id: nameField
                                    width: (formColumn.width - removeButton.width - Theme.spacingS * 2) * 0.42
                                    outlined: true
                                    placeholderText: modal.mode === "remote" ? "Authorization" : "GITHUB_PERSONAL_ACCESS_TOKEN"
                                    text: secretRow.modelData.name
                                    onTextChanged: modal.rowName(secretRow.index, text)
                                }

                                DTextField {
                                    width: formColumn.width - nameField.width - removeButton.width - secretRow.spacing * 2
                                    outlined: true
                                    // A stored value is never handed back, so the placeholder is the
                                    // only sign that this row already has one.
                                    placeholderText: modal.storedKeys.indexOf(secretRow.modelData.name) >= 0
                                        ? "••••••••"
                                        : (modal.mode === "remote" ? "Bearer …" : "value")
                                    echoMode: TextInput.Password
                                    showPasswordToggle: true
                                    text: secretRow.modelData.value
                                    onTextChanged: modal.rowValue(secretRow.index, text)
                                }

                                DActionButton {
                                    id: removeButton
                                    anchors.verticalCenter: parent.verticalCenter
                                    iconName: "delete_outline"
                                    tooltipText: I18n.tr("Remove", "mcp form remove row tooltip")
                                    onClicked: modal.removeRow(secretRow.index)
                                }
                            }
                        }

                        DButton {
                            text: modal.mode === "remote"
                                ? I18n.tr("Add header", "mcp form add a header row")
                                : I18n.tr("Add variable", "mcp form add an environment variable row")
                            variant: "text"
                            iconName: "add"
                            buttonHeight: Theme.buttonHeightXS
                            onClicked: modal.addRow()
                        }

                        StyledText {
                            text: I18n.tr("Empty values keep what is stored. Removing a row deletes it.", "mcp form advanced section hint")
                            font.pixelSize: Theme.fontSizeSmall
                            color: Theme.surfaceVariantText
                            width: parent.width
                            wrapMode: Text.WordWrap
                        }
                    }

                    // Auto-approve is an "I know what I am doing" switch, so the description is blunt.
                    Row {
                        width: parent.width
                        spacing: Theme.spacingM

                        Column {
                            width: parent.width - autoToggle.width - Theme.spacingM
                            spacing: 2

                            StyledText {
                                text: I18n.tr("Do not ask before each tool call", "mcp auto approve label")
                                font.pixelSize: Theme.fontSizeMedium
                                color: Theme.surfaceText
                                width: parent.width
                                wrapMode: Text.WordWrap
                            }

                            StyledText {
                                text: I18n.tr("Dux Code cannot tell what a server will do. Only turn this on if you trust it.", "mcp auto approve description")
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceVariantText
                                width: parent.width
                                wrapMode: Text.WordWrap
                            }
                        }

                        DToggle {
                            id: autoToggle
                            anchors.verticalCenter: parent.verticalCenter
                            hideText: true
                            checked: modal.autoApprove
                            onToggled: value => modal.autoApprove = value
                        }
                    }

                    Row {
                        visible: modal.editing
                        width: parent.width
                        spacing: Theme.spacingM

                        Column {
                            width: parent.width - enabledToggle.width - Theme.spacingM
                            spacing: 2

                            StyledText {
                                text: I18n.tr("Enabled", "mcp enabled label")
                                font.pixelSize: Theme.fontSizeMedium
                                color: Theme.surfaceText
                                width: parent.width
                            }

                            StyledText {
                                text: I18n.tr("Turning this off stops the server but keeps its settings.", "mcp enabled description")
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceVariantText
                                width: parent.width
                                wrapMode: Text.WordWrap
                            }
                        }

                        DToggle {
                            id: enabledToggle
                            anchors.verticalCenter: parent.verticalCenter
                            hideText: true
                            checked: modal.enabled
                            onToggled: value => modal.enabled = value
                        }
                    }

                    StyledText {
                        visible: modal.errorText !== ""
                        text: modal.errorText
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.error
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    Row {
                        spacing: Theme.spacingS

                        DButton {
                            text: modal.editing ? I18n.tr("Save", "mcp save button when editing") : I18n.tr("Add", "mcp add button")
                            enabled: !modal.busy
                            onClicked: modal.save()
                        }
                    }
                }
            }
        }
    }

    Shortcut {
        sequence: "Escape"
        enabled: modal.visible
        onActivated: modal.hide()
    }

    // Without this the header's `controls` is a dangling reference: DWindowHeader then treats the
    // window as compositor-decorated and drops the title, the close button and the drag area, so
    // the only way out of the form was to save it.
    FloatingWindowControls {
        id: windowControls

        targetWindow: modal
        z: 101
    }
}
