import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets

// Add or edit a search backend.
//
// Same flow as providers: pick the type, then fill the fields it needs. The types differ only
// in whether they want a URL or a key, so one form shows the fields per type.
//
// "Test" runs a real query (query = quickshell). A bare ping is not enough: SearXNG answers 200
// with JSON output disabled, and the user would think it works until the model finds nothing.
FloatingWindow {
    id: modal

    // Empty while picking a type; non-empty once the form is showing.
    property string kind: ""
    property bool editing: false
    property string editId: ""

    property string baseUrl: ""
    property string apiKey: ""
    property bool hasStoredKey: false
    property bool busy: false
    property string errorText: ""
    property string testText: ""
    property bool testOk: false

    readonly property var kinds: Backend.searchKinds
    readonly property var kindInfo: {
        for (let i = 0; i < kinds.length; i++) {
            if (kinds[i].id === kind)
                return kinds[i];
        }
        return null;
    }

    readonly property string formTitle: {
        if (kind === "")
            return "";
        return kindInfo ? kindInfo.label : kind;
    }

    function reset() {
        kind = "";
        editing = false;
        editId = "";
        baseUrl = "";
        apiKey = "";
        hasStoredKey = false;
        busy = false;
        errorText = "";
        testText = "";
        testOk = false;
    }

    function show() {
        reset();
        visible = true;
    }

    function showFor(id) {
        reset();
        const p = Backend.searchProviderById(id);
        if (!p)
            return;
        editing = true;
        editId = id;
        kind = p.type;
        baseUrl = p.baseUrl || "";
        apiKey = "";
        hasStoredKey = p.hasKey === true;
        visible = true;
    }

    function back() {
        kind = "";
        errorText = "";
        testText = "";
    }

    function hide() {
        visible = false;
    }

    function _params() {
        // No name field: the backend's own name is used (SearXNG / Brave Search / Tavily).
        // Renaming is rare enough to do in the config file.
        const out = {
            "type": kind,
            "baseUrl": String(baseUrl).trim(),
            "apiKey": String(apiKey).trim()
        };
        if (editing)
            out.id = editId;
        return out;
    }

    function runTest() {
        if (busy)
            return;
        busy = true;
        errorText = "";
        testText = "";

        const params = _params();
        if (editing)
            params.id = editId; // lets the daemon reuse the stored key

        Backend.testSearch(params, (res, err) => {
            busy = false;
            if (err) {
                testOk = false;
                errorText = err;
                return;
            }
            testOk = true;
            const n = res && res.count !== undefined ? res.count : 0;
            const ms = res && res.elapsedMs !== undefined ? res.elapsedMs : 0;
            testText = I18n.tr("Connected · %1 results · %2 ms", "search test success message").arg(n).arg(ms);
        });
    }

    function save() {
        if (busy)
            return;
        busy = true;
        errorText = "";

        Backend.saveSearchProvider(_params(), (res, err) => {
            busy = false;
            if (err) {
                errorText = err;
                return;
            }
            ToastService.info(editing ? I18n.tr("Saved", "toast after saving search backend") : I18n.tr("Search backend added", "toast after adding search backend"));
            hide();
        });
    }

    title: kind === "" ? I18n.tr("Add search backend", "search backend modal window title") : formTitle
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

    // Closing this window from the compositor (super+q, a close button) only emits closed and leaves
    // visible true, so the next show brings it back. See the long note in MCPServerModal.
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

            // Must come after the header, or the drag area swallows the button.
            DActionButton {
                anchors.left: parent.left
                anchors.leftMargin: Theme.spacingS
                anchors.verticalCenter: parent.verticalCenter
                visible: modal.kind !== "" && !modal.editing
                iconName: "arrow_back"
                buttonSize: Theme.buttonHeightXXS
                z: 2
                Accessible.name: I18n.tr("Back", "back button in search setup")
                onClicked: modal.back()
            }
        }

        Item {
            width: parent.width
            height: parent.height - headerRow.height
            clip: true

            Loader {
                anchors.fill: parent
                anchors.margins: Theme.spacingL
                sourceComponent: modal.kind === "" ? kindPicker : kindForm
            }
        }
    }

    Shortcut {
        sequence: "Escape"
        enabled: modal.visible
        onActivated: modal.hide()
    }

    Toast {
        anchors.fill: parent
        z: 102
    }

    FloatingWindowControls {
        id: windowControls

        targetWindow: modal
        z: 101
    }

    // --- type picker ---
    Component {
        id: kindPicker

        DFlickable {
            clip: true
            contentWidth: width
            contentHeight: pickerColumn.implicitHeight + Theme.spacingL * 2

            Column {
                id: pickerColumn

                width: parent.width
                spacing: Theme.spacingM

                StyledText {
                    text: I18n.tr("Choose a backend", "heading for search backend picker")
                    font.pixelSize: Theme.fontSizeLarge
                    font.weight: Theme.fontWeightMedium
                    color: Theme.surfaceText
                    width: parent.width
                }

                StyledText {
                    text: I18n.tr("Keys are stored in the config file on this machine. Nothing is shared.", "subtitle under search backend picker")
                    font.pixelSize: Theme.fontSizeMedium
                    color: Theme.surfaceVariantText
                    width: parent.width
                    wrapMode: Text.WordWrap
                }

                Repeater {
                    model: modal.kinds

                    delegate: StyledRect {
                        id: kindCard

                        required property var modelData

                        // One instance per type: with no name field, two rows called "Tavily"
                        // would be indistinguishable.
                        readonly property bool alreadyAdded: {
                            for (let i = 0; i < Backend.searchProviders.length; i++) {
                                if (Backend.searchProviders[i].type === modelData.id)
                                    return true;
                            }
                            return false;
                        }

                        width: parent.width
                        height: rowBody.implicitHeight + Theme.spacingM * 2
                        color: kindHover.hovered && !alreadyAdded ? Theme.surfaceContainerHigh : Theme.surfaceContainerLow
                        radius: Theme.cornerRadiusL

                        Behavior on color {
                            ColorAnimation {
                                duration: Theme.expressiveDurations.expressiveFastSpatial
                                easing.type: Easing.BezierSpline
                                easing.bezierCurve: Theme.expressiveCurves.standard
                            }
                        }

                        Row {
                            id: rowBody

                            anchors.left: parent.left
                            anchors.right: parent.right
                            anchors.verticalCenter: parent.verticalCenter
                            anchors.leftMargin: Theme.spacingM
                            anchors.rightMargin: Theme.spacingM
                            spacing: Theme.spacingM

                            Column {
                                id: kindColumn

                                width: parent.width - (addedRow.visible ? addedRow.width + parent.spacing : 0)
                                anchors.verticalCenter: parent.verticalCenter
                                spacing: Theme.spacingXS

                                StyledText {
                                    text: modelData.label
                                    font.pixelSize: Theme.fontSizeMedium
                                    font.weight: Theme.fontWeightMedium
                                    color: kindCard.alreadyAdded ? Theme.surfaceVariantText : Theme.surfaceText
                                    width: parent.width
                                }

                                StyledText {
                                    text: modelData.hint
                                    font.pixelSize: Theme.fontSizeSmall
                                    color: Theme.surfaceVariantText
                                    width: parent.width
                                    wrapMode: Text.WordWrap
                                }
                            }

                            Row {
                                id: addedRow

                                anchors.verticalCenter: parent.verticalCenter
                                spacing: Theme.spacingXS
                                visible: kindCard.alreadyAdded

                                DIcon {
                                    anchors.verticalCenter: parent.verticalCenter
                                    name: "check"
                                    size: Theme.iconSizeSmall
                                    color: Theme.primary
                                }

                                StyledText {
                                    anchors.verticalCenter: parent.verticalCenter
                                    text: I18n.tr("Added", "search backend badge when this kind already exists")
                                    font.pixelSize: Theme.fontSizeSmall
                                    color: Theme.primary
                                }
                            }
                        }

                        HoverHandler {
                            id: kindHover
                            cursorShape: kindCard.alreadyAdded ? Qt.ArrowCursor : Qt.PointingHandCursor
                        }

                        StateLayer {
                            stateColor: Theme.surfaceText
                            disabled: kindCard.alreadyAdded
                            enabled: !kindCard.alreadyAdded
                            cornerRadius: kindCard.radius
                            onClicked: modal.kind = kindCard.modelData.id
                        }
                    }
                }
            }
        }
    }

    // --- form ---
    Component {
        id: kindForm

        DFlickable {
            clip: true
            contentWidth: width
            contentHeight: formColumn.implicitHeight + Theme.spacingL * 2

            Column {
                id: formColumn

                width: parent.width
                spacing: Theme.spacingM

                StyledText {
                    visible: modal.kindInfo !== null
                    text: modal.kindInfo ? modal.kindInfo.hint : ""
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                    width: parent.width
                    wrapMode: Text.WordWrap
                }

                DTextField {
                    width: parent.width
                    visible: modal.kindInfo !== null && modal.kindInfo.needUrl === true
                    outlined: true
                    labelText: I18n.tr("Instance URL", "search backend instance url field label")
                    placeholderText: "http://localhost:8888"
                    leftIconName: "link"
                    text: modal.baseUrl
                    onTextChanged: modal.baseUrl = text
                    Component.onCompleted: forceActiveFocus()
                }

                DTextField {
                    width: parent.width
                    visible: modal.kindInfo !== null && modal.kindInfo.needKey === true
                    outlined: true
                    labelText: modal.hasStoredKey ? I18n.tr("API key — leave empty to keep the saved one", "search key label when a key is stored") : I18n.tr("API key", "search key field label")
                    placeholderText: modal.hasStoredKey ? "••••••••" : ""
                    leftIconName: "key"
                    echoMode: TextInput.Password
                    showPasswordToggle: true
                    text: modal.apiKey
                    onTextChanged: modal.apiKey = text
                }

                StyledText {
                    visible: modal.testText !== ""
                    text: modal.testText
                    font.pixelSize: Theme.fontSizeSmall
                    color: modal.testOk ? Theme.primary : Theme.error
                    width: parent.width
                    wrapMode: Text.WordWrap
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
                        text: I18n.tr("Test connection", "search backend test button")
                        variant: "tonal"
                        enabled: !modal.busy
                        onClicked: modal.runTest()
                    }

                    DButton {
                        text: modal.editing ? I18n.tr("Save", "search backend save button when editing") : I18n.tr("Add", "search backend add button")
                        enabled: !modal.busy
                        onClicked: modal.save()
                    }
                }
            }
        }
    }
}
