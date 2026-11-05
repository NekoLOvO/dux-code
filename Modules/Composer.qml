import QtQuick
import QtQuick.Controls
import Quickshell
import qs.Common
import qs.DCommon.Common
import qs.Widgets
import qs.DCommon.Widgets
import qs.DCommon.FileBrowser
import qs.Services

// Composer: input area on top, toolbar below (attachments / model / reasoning / send).
Item {
    id: root

    signal submitted(string text, var attachments)

    readonly property bool busy: Chat.busy
    readonly property bool hasText: input.text.trim().length > 0

    // The chat window, passed down by ChatContent. The two pickers below are FloatingWindows and
    // need it as their parentModal, or a tiling compositor tiles them instead of floating them.
    property var hostWindow: null

    // Pending attachments, absolute paths. Selection happens here; the daemon records them on
    // the message and the tool layer reads the files.
    property var attachments: []

    readonly property bool hasAttachments: attachments.length > 0
    readonly property bool canSend: hasText || hasAttachments

    // --- reasoning depth ---
    // Level names are ours, not any provider's parameter names. Which ones a model supports
    // comes from the daemon (config.ReasoningLevels); guessing here would mean a 400.
    readonly property var reasoningChoices: [
        {
            "value": "auto",
            "label": I18n.tr("Auto", "reasoning level: let the provider decide")
        },
        {
            "value": "none",
            // context must match the zh_TW.json key exactly, or the lookup fails and English leaks.
            "label": I18n.tr("Off", "reasoning level: disabled")
        },
        {
            "value": "minimal",
            "label": I18n.tr("Minimal", "reasoning level: minimal")
        },
        {
            "value": "low",
            "label": I18n.tr("Low", "reasoning level: low")
        },
        {
            "value": "medium",
            "label": I18n.tr("Medium", "reasoning level: medium")
        },
        {
            "value": "high",
            "label": I18n.tr("High", "reasoning level: high")
        },
        {
            "value": "xhigh",
            "label": I18n.tr("Extra high", "reasoning level: extra high")
        },
        {
            "value": "max",
            "label": I18n.tr("Max", "reasoning level: max")
        }
    ]

    // Levels the current model supports. Empty means unsupported and the whole menu is hidden:
    // a menu holding only "auto" is a dead control.
    readonly property var reasoningLevels: Models.current ? (Models.current.reasoning || []) : []
    readonly property bool hasReasoning: Models.currentId !== "" && reasoningLevels.length > 0
    // Only what the model actually supports, plus auto.
    readonly property var reasoningOptions: reasoningChoices.filter(c => c.value === "auto" || reasoningLevels.indexOf(c.value) >= 0)

    readonly property int maxWidth: 820
    readonly property int barHeight: Theme.buttonHeightM

    implicitHeight: box.height + Theme.spacingXL

    // Working directory passed to the daemon as cwd.
    readonly property string workingDir: SettingsData.workingDirectory !== "" ? SettingsData.workingDirectory : Quickshell.env("HOME")
    // Change the working directory: persist it for new sessions and update the open one
    // immediately (the daemon reads it when building the system prompt).
    function applyWorkingDir(path) {
        if (!path || path === "")
            return;
        SettingsData.workingDirectory = path;
        if (Sessions.activeId !== "")
            Backend.sendRequest("session.setWorkingDirectory", {
                "sessionId": Sessions.activeId,
                "path": path
            });
    }

    readonly property string shortWorkingDir: {
        const home = Quickshell.env("HOME");
        return (home && workingDir.indexOf(home) === 0) ? "~" + workingDir.slice(home.length) : workingDir;
    }

    // What this conversation has spent so far, counted by the daemon from API-reported usage —
    // every request, so tool loops count their re-sends too. No cost display: price tables need
    // maintenance and are inaccurate (tiered pricing plus cache discounts).
    //
    // Millions are expected here: a coding turn with a hundred tool calls re-sends the context a
    // hundred times, so one turn can add several million on its own.
    readonly property string usageText: {
        const n = Chat.sessionTokens;
        if (n <= 0)
            return "";
        if (n >= 1000000)
            return (n / 1000000).toFixed(1) + "M";
        if (n >= 1000)
            return (n / 1000).toFixed(1) + "k";
        return "" + n;
    }


    function openExpanded() {
        bigInput.text = input.text;
        expandDialog.open();
    }

    // Send from the full-window editor. Ids are lexical names, not properties on root.
    function sendExpanded() {
        if (String(bigInput.text).trim().length === 0 && !root.hasAttachments)
            return;
        input.text = bigInput.text;
        bigInput.text = "";
        expandDialog.close();
        root.submit();
    }

    function addAttachments(paths) {
        if (!paths)
            return;
        const next = root.attachments.slice();
        for (let i = 0; i < paths.length; i++) {
            const p = String(paths[i] || "");
            if (p === "" || next.indexOf(p) >= 0)
                continue;
            next.push(p);
        }
        root.attachments = next;
    }

    function removeAttachment(path) {
        root.attachments = root.attachments.filter(p => p !== path);
    }

    function fileName(path) {
        const s = String(path);
        const i = s.lastIndexOf("/");
        return i >= 0 ? s.slice(i + 1) : s;
    }

    // Pick a Material Symbol by extension. No thumbnails: that needs another async pipeline,
    // and the filename is what the user needs. Only these types, matching the daemon's
    // sniffImage (no svg — no provider accepts it).
    function isImagePath(path) {
        const name = fileName(path).toLowerCase();
        const dot = name.lastIndexOf(".");
        const ext = dot >= 0 ? name.slice(dot + 1) : "";
        return ext === "png" || ext === "jpg" || ext === "jpeg" || ext === "gif" || ext === "webp";
    }

    function fileIcon(path) {
        const name = fileName(path).toLowerCase();
        const dot = name.lastIndexOf(".");
        const ext = dot >= 0 ? name.slice(dot + 1) : "";
        switch (ext) {
        case "png":
        case "jpg":
        case "jpeg":
        case "gif":
        case "webp":
        case "svg":
        case "bmp":
            return "image";
        case "pdf":
            return "picture_as_pdf";
        case "md":
        case "txt":
            return "description";
        case "conf":
        case "cfg":
        case "ini":
        case "env":
            return "settings";
        case "json":
        case "yaml":
        case "yml":
        case "toml":
        case "csv":
            return "data_object";
        case "go":
        case "js":
        case "ts":
        case "qml":
        case "py":
        case "rs":
        case "sh":
        case "c":
        case "cc":
        case "cpp":
        case "h":
            return "code";
        case "zip":
        case "tar":
        case "gz":
        case "xz":
        case "7z":
            return "folder_zip";
        case "mp4":
        case "mkv":
        case "mov":
        case "webm":
            return "movie";
        case "mp3":
        case "wav":
        case "flac":
        case "ogg":
            return "audio_file";
        default:
            return "attach_file";
        }
    }

    function submit() {
        const t = input.text;
        const paths = root.attachments.slice();
        // Nothing to send without text or attachments.
        if (String(t).trim().length === 0 && paths.length === 0)
            return;
        input.text = "";
        root.attachments = [];
        root.submitted(t, paths);
    }

    // ---------- body ----------
    // Frame is the library's DCard, so padding and radius come from Theme; contents are a Column.
    DCard {
        id: box

        anchors.bottom: parent.bottom
        anchors.bottomMargin: Theme.spacingM
        anchors.horizontalCenter: parent.horizontalCenter
        width: Math.min(parent.width - Theme.spacingXL * 2, root.maxWidth)
        height: composerColumn.implicitHeight + pad * 2
        pad: Theme.spacingS
        // Appearance is entirely DCard's (radius / cardSurface / 1 px layer outline), so a theme
        // change needs no edit here.

        Column {
            id: composerColumn

            width: parent.width
            spacing: Theme.spacingXS

            // ---- attachments (above the field) ----
            Flow {
                id: attachRow

                width: parent.width
                spacing: Theme.spacingXS
                visible: root.hasAttachments

                Repeater {
                    model: root.attachments

                    delegate: Rectangle {
                        id: attachChip

                        required property string modelData

                        readonly property string icon: root.fileIcon(attachChip.modelData)
                        readonly property bool isImage: root.isImagePath(attachChip.modelData)

                        // Filename chip: chipSurface is one step above the composer surface, plus a
                        // 0.18 outline so the boundary survives dark themes. Width uses
                        // chipRow.implicitWidth — chipRow.width + centerIn would make the width depend
                        // on itself and Qt would compute 0.
                        width: chipRow.implicitWidth + Theme.spacingS * 2
                        height: Theme.buttonHeightXS
                        radius: Theme.cornerRadiusS
                        color: Theme.chipSurface
                        border.width: 1
                        border.color: Theme.outlineStrong

                        Row {
                            id: chipRow

                            x: Theme.spacingS
                            anchors.verticalCenter: parent.verticalCenter
                            spacing: Theme.spacingXS

                            // Thumbnail for images: two pasted screenshots are indistinguishable by
                            // name. Wrapped in an Item because Row children cannot be anchored.
                            Item {
                                width: Theme.iconSizeSmall + 4
                                height: Theme.buttonHeightXS

                                StyledText {
                                    visible: !attachChip.isImage
                                    anchors.centerIn: parent
                                    text: attachChip.icon
                                    font.family: Fonts.icons
                                    font.pixelSize: Theme.iconSizeSmall
                                    color: Theme.surfaceVariantText
                                }

                                Image {
                                    visible: attachChip.isImage
                                    anchors.centerIn: parent
                                    width: Theme.iconSizeSmall + 4
                                    height: width
                                    asynchronous: true
                                    fillMode: Image.PreserveAspectCrop
                                    sourceSize.width: 48
                                    source: attachChip.isImage ? "file://" + attachChip.modelData : ""
                                }
                            }

                            StyledText {
                                height: Theme.buttonHeightXS
                                verticalAlignment: Text.AlignVCenter
                                width: Math.min(implicitWidth, 200)
                                text: root.fileName(attachChip.modelData)
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceText
                                elide: Text.ElideMiddle
                                maximumLineCount: 1
                            }

                            // Row children must not use anchors (even a nested centerIn counts), or the
                            // whole Row breaks and children stack at x=0. The close button is a fixed-size
                            // Item with anchors inside.
                            Item {
                                width: Math.round(Theme.buttonHeightXS * 0.66)
                                height: Theme.buttonHeightXS

                                StateLayer {
                                    anchors.fill: parent
                                    cornerRadius: width / 2
                                    stateColor: Theme.surfaceText
                                    onClicked: root.removeAttachment(attachChip.modelData)
                                }

                                StyledText {
                                    anchors.centerIn: parent
                                    text: "close"
                                    font.family: Fonts.icons
                                    font.pixelSize: Theme.fontSizeSmall
                                    color: Theme.surfaceVariantText
                                }
                            }
                        }
                    }
                }
            }

            // ---- input ----
            // ComposerField gives DTextField's visuals with multi-line growth; focus ring,
            // placeholder and newline rules belong to it.
            ComposerField {
                id: input

                width: parent.width
                minimumLines: 1
                maximumLines: 6
                backgroundColor: "transparent"
                cornerRadius: Theme.cornerRadiusXS
                focusedBorderWidth: Theme.outlineWidthFocused
                hidePlaceholderOnFocus: true
                placeholderText: I18n.tr("Ask, search, or brainstorm…   Shift+Enter for a new line", "composer placeholder with shortcut hint")
                onAccepted: root.submit()
                // Pasted files become attachments instead of a path string.
                onFilesPasted: paths => root.addAttachments(paths)
            }

            // ---- toolbar ----
            Item {
                id: bottomBar

                width: parent.width
                height: root.barHeight

                DIconButton {
                    id: attachBtn

                    anchors.left: parent.left
                    anchors.leftMargin: Theme.spacingXS
                    anchors.verticalCenter: parent.verticalCenter
                    size: "s"
                    iconName: "add"
                    Accessible.name: I18n.tr("Attach files", "composer button that opens the file picker")
                    onClicked: {
                        filePicker.startPath = root.workingDir;
                        filePicker.open();
                    }
                }

                DIconButton {
                    id: expandBtn

                    anchors.left: attachBtn.right
                    anchors.verticalCenter: parent.verticalCenter
                    size: "s"
                    iconName: "open_in_full"
                    Accessible.name: I18n.tr("Expand editor", "composer button that opens the full screen editor")
                    onClicked: root.openExpanded()
                }

                // ---- web access ----
                // One switch for search, page fetch and browser: splitting it invites "I turned the
                // web off, why is it still fetching pages".
                // Usable without a search backend: fetching and the browser do not need one.
                DIconButton {
                    id: webSearchBtn

                    readonly property bool active: Backend.webSearch
                    readonly property bool hasSearch: Backend.searchProviders.length > 0

                    anchors.left: expandBtn.right
                    anchors.leftMargin: Theme.spacingXS
                    anchors.verticalCenter: parent.verticalCenter
                    size: "s"
                    iconName: "travel_explore"
                    iconFilled: active
                    variant: active ? "tonal" : "standard"
                    focusPolicy: Qt.NoFocus
                    Accessible.name: I18n.tr("Web access", "composer toggle for web access")
                    Accessible.description: active ? I18n.tr("On", "web access toggle state") : I18n.tr("Off", "web access toggle state")
                    tooltipText: active ? (hasSearch ? I18n.tr("Web access is on — search, page fetching and the browser", "web access tooltip when on and a search backend exists") : I18n.tr("Web access is on — page fetching and the browser only (no search backend yet)", "web access tooltip when on but no search backend")) : I18n.tr("Web access is off — no search, fetching or browser for this and later messages", "web access tooltip when off")
                    onClicked: Backend.setWebSearch(!Backend.webSearch)
                }

                // ---- working directory ----
                // DButton handles hover, ripple, cursor and tooltip, matching the buttons around it.
                DButton {
                    id: dirChip

                    anchors.left: webSearchBtn.right
                    anchors.leftMargin: Theme.spacingXS
                    anchors.verticalCenter: parent.verticalCenter
                    buttonHeight: Theme.buttonHeightXS
                    maximumWidth: 200
                    iconName: "folder"
                    iconSize: Theme.iconSizeSmall
                    text: root.shortWorkingDir
                    backgroundColor: Theme.surfaceContainerHighest
                    textColor: Theme.surfaceVariantText
                    tooltipText: root.workingDir
                    focusPolicy: Qt.NoFocus
                    // Opens the picker directly; the intermediate menu only offered "choose folder /
                    // use home", and home is reachable inside the picker anyway.
                    onClicked: {
                        dirPicker.startPath = root.workingDir;
                        dirPicker.open();
                    }
                }

                // ---- send / stop ----
                DIconButton {
                    id: sendBtn

                    readonly property bool idle: !root.busy && !root.canSend

                    anchors.right: parent.right
                    anchors.rightMargin: Theme.spacingXS
                    anchors.verticalCenter: parent.verticalCenter
                    size: "s"
                    variant: "filled"
                    iconName: root.busy ? "stop" : "arrow_upward"

                    enabled: root.busy || root.canSend
                    containerColor: root.busy ? Theme.errorContainer : Theme.primary
                    contentColor: root.busy ? Theme.error : (idle ? Theme.surfaceVariantText : Theme.primaryText)

                    scale: pressed ? 0.86 : 1.0

                    Behavior on scale {
                        enabled: Theme.animationsEnabled

                        NumberAnimation {
                            duration: Theme.expressiveDurations.expressiveEffects
                            easing.type: Easing.BezierSpline
                            easing.bezierCurve: Theme.expressiveCurves.standard
                        }
                    }

                    HoverHandler {
                        cursorShape: Qt.PointingHandCursor
                    }

                    onClicked: {
                        if (root.busy) {
                            Chat.cancel();
                            return;
                        }
                        root.submit();
                    }
                }

                // ---- token usage ----
                DDetailChip {
                    id: usageChip

                    visible: root.usageText !== ""
                    // Shifts left when the reasoning menu is present so they do not overlap.
                    anchors.right: root.hasReasoning ? reasoningDropdown.left : modelDropdown.left
                    anchors.rightMargin: Theme.spacingS
                    anchors.verticalCenter: parent.verticalCenter
                    // Left untranslated: "usage" would blur with context length and cost, and
                    // "tokens" is the common term. The value is the whole session's spend, so it
                    // keeps growing and does not fall when compaction shrinks the context.
                    label: "Tokens"
                    value: root.usageText
                }

                // ---- reasoning depth ----
                // Left of the model picker: it qualifies how this model should think, which is part of
                // the same decision. Hidden entirely when the model has no levels.
                DDropdown {
                    id: reasoningDropdown

                    visible: root.hasReasoning
                    anchors.right: modelDropdown.left
                    anchors.rightMargin: Theme.spacingXS
                    anchors.verticalCenter: parent.verticalCenter

                    dropdownWidth: 128
                    showLabel: true
                    openUpwards: true
                    alignPopupRight: true
                    maxPopupHeight: 240

                    options: root.reasoningOptions.map(c => c.label)
                    optionValues: root.reasoningOptions.map(c => c.value)
                    // A stored value may no longer be offered (config changed); fall back to auto
                    // rather than showing a blank menu.
                    currentValue: {
                        const want = SettingsData.reasoningLevel(Models.currentId);
                        return root.reasoningOptions.some(o => o.value === want) ? want : "auto";
                    }

                    onValueChanged: value => SettingsData.setReasoningLevel(Models.currentId, value)
                }

                // ---- model picker: the settings page's DDropdown (native animation, correct contrast) ----
                DDropdown {
                    id: modelDropdown

                    anchors.right: sendBtn.left
                    anchors.rightMargin: Theme.spacingS
                    anchors.verticalCenter: parent.verticalCenter

                    dropdownWidth: 176
                    showLabel: true
                    openUpwards: true
                    alignPopupRight: true
                    maxPopupHeight: 320

                    options: Models.available.map(m => m.label)
                    optionValues: Models.available.map(m => m.id)
                    currentValue: Models.currentId

                    onValueChanged: value => Models.select(value)
                }
            }
        }
    }

    // Library folder picker, the same one dux-calendar's "Choose calendar directory" uses. Its
    // data comes from Host.files -> Services/FilesService.qml -> the daemon's files.*, and is
    // empty when the daemon does not advertise the capability.
    FileBrowserModal {
        id: dirPicker

        parentModal: root.hostWindow
        folderMode: true
        browserTitle: I18n.tr("Choose working directory", "working directory picker window title")
        browserType: "workingdir"

        onAccepted: paths => {
            if (paths && paths.length > 0)
                root.applyWorkingDir(paths[0]);
        }
    }

    // The same picker for attachments, switched to files and multi-select.
    FileBrowserModal {
        id: filePicker

        parentModal: root.hostWindow
        folderMode: false
        multiple: true
        browserTitle: I18n.tr("Attach files", "attachment picker window title")
        browserType: "attachment"

        onAccepted: paths => root.addAttachments(paths)
    }

    // ---- full-window editor ----
    DankOverlayDialog {
        id: expandDialog

        title: I18n.tr("Compose", "expanded editor dialog title")
        maximumWidth: 1000

        onAccepted: root.sendExpanded()
        onRejected: {
            // Cancel keeps the draft in the field. close() is called here because this handler
            // replaces DankOverlayDialog's own onRejected.
            if (bigInput.text !== root.input.text)
                input.text = bigInput.text;
            expandDialog.close();
        }

        // Fixed ten-line box: past that the field scrolls instead of clipping. Width comes
        // from the dialog's content column, so long lines wrap instead of running past it.
        ComposerField {
            id: bigInput

            minimumLines: 10
            maximumLines: 10
            backgroundColor: "transparent"
            borderWidth: 0
            focusedBorderWidth: 0
            contentPadding: 0
            verticalPadding: 0
            textColor: Theme.surfaceText
            placeholderText: I18n.tr("Ask, search, or brainstorm…", "expanded composer placeholder")
            placeholderColor: Theme.withAlpha(Theme.surfaceVariantText, 0.55)
            hidePlaceholderOnFocus: false
            font.pixelSize: Theme.fontSizeLarge
            onAccepted: root.sendExpanded()
            onEscapePressed: expandDialog.rejected()
            onFilesPasted: paths => root.addAttachments(paths)
        }

        actions: [
            DButton {
                text: I18n.tr("Cancel", "expanded editor cancel button")
                variant: "tonal"
                onClicked: expandDialog.rejected()
            },
            DButton {
                text: I18n.tr("Send", "expanded editor send button")
                enabled: (bigInput.text.trim().length > 0 || root.hasAttachments) && !root.busy
                onClicked: root.sendExpanded()
            }
        ]
    }

    Component.onCompleted: input.forceActiveFocus()
}
