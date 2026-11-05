import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets

// Add or edit an MCP server.
//
// A single page: the fields are "how to start it", with no type to pick first. env is not
// editable here — those are tokens meant to be typed, and routing them through the UI would
// turn every save into a chance to leak a key. They stay in config.json.
FloatingWindow {
    id: modal

    property bool editing: false
    property string editId: ""
    property bool busy: false
    property string errorText: ""

    property string label: ""
    // A non-empty url means remote (streamable HTTP); command is then unused.
    property string url: ""
    property string command: ""
    property string argsText: ""
    property bool autoApprove: false
    property bool enabled: true
    // Env var names already configured, shown when editing (keys only, never values).
    property var envKeys: []

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
        url = "";
        command = "";
        argsText = "";
        autoApprove = false;
        enabled = true;
        envKeys = [];
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
        url = s.url || "";
        command = s.command || "";
        argsText = joinArgs(s.args);
        autoApprove = s.autoApprove === true;
        enabled = s.disabled !== true;
        envKeys = s.envKeys || [];
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
        const remote = String(url).trim();
        const cmd = String(command).trim();
        if (remote === "" && cmd === "") {
            errorText = I18n.tr("Pick a command to run, or enter a server URL.", "mcp form error when neither command nor url is filled");
            return;
        }
        busy = true;
        errorText = "";

        const params = {
            "label": String(label).trim(),
            "url": remote,
            "command": remote === "" ? cmd : "",
            "args": remote === "" ? splitArgs(argsText) : [],
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
                        text: I18n.tr("An MCP server offers tools over stdio or streamable HTTP. Dux Code connects to it and hands its tools to the model. A local server runs as you do, outside the workspace sandbox.", "mcp form intro")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
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

                    DTextField {
                        width: parent.width
                        outlined: true
                        labelText: I18n.tr("Server URL (remote)", "mcp form url field label")
                        placeholderText: "https://mcp.example.com/mcp"
                        leftIconName: "link"
                        text: modal.url
                        onTextChanged: modal.url = text
                    }

                    StyledText {
                        visible: modal.url.trim() !== ""
                        text: I18n.tr("Remote servers speak streamable HTTP, so nothing runs on this machine. Leave this empty to launch a local command instead.", "mcp form url hint")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    DTextField {
                        width: parent.width
                        outlined: true
                        visible: modal.url.trim() === ""
                        labelText: I18n.tr("Command", "mcp form command field label")
                        placeholderText: "npx"
                        leftIconName: "terminal"
                        text: modal.command
                        onTextChanged: modal.command = text
                    }

                    DTextField {
                        width: parent.width
                        outlined: true
                        visible: modal.url.trim() === ""
                        labelText: I18n.tr("Arguments", "mcp form args field label")
                        placeholderText: "-y @modelcontextprotocol/server-filesystem /home/you"
                        text: modal.argsText
                        onTextChanged: modal.argsText = text
                    }

                    StyledText {
                        visible: modal.url.trim() === ""
                        text: I18n.tr("Separate arguments with spaces. Wrap one in double quotes if it contains spaces.", "mcp form args hint")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
                    }

                    // Keys only: the values are tokens and must not leave the daemon to be shown.
                    StyledText {
                        visible: modal.envKeys.length > 0
                        text: I18n.tr("Environment: %1 (values stay in config.json)", "mcp form env keys hint").arg(modal.envKeys.join(", "))
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                        wrapMode: Text.WordWrap
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
