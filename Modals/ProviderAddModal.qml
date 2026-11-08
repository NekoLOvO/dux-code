import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets

// Add or edit a provider.
//
// Same shape as dux-calendar's AccountAddModal: a standalone FloatingWindow, DWindowHeader
// owning the title and close button, and a Loader swapping pages inside the window. Height is
// fixed, so picking a template and fetching models never reflow it.
FloatingWindow {
    id: providerModal

    // Empty while the service picker is showing.
    property string selectedProvider: ""
    property bool editing: false

    property string label: ""
    property string baseUrl: ""
    property string apiKey: ""
    property var modelList: []
    property var enabledMap: ({})
    property bool busy: false
    property string errorText: ""
    property bool fetchedNow: false
    property bool hasStoredKey: false
    // nativeSearch is the user's choice; canNativeSearch says the type has built-in search
    // at all.
    property bool nativeSearch: true
    property bool canNativeSearch: false

    readonly property var templates: Backend.providerTemplates

    // Template for the current selection: by id when adding, by type when editing.
    readonly property var template: {
        if (selectedProvider === "")
            return null;
        for (let i = 0; i < templates.length; i++) {
            if (templates[i].id === selectedProvider)
                return templates[i];
        }
        if (editing) {
            const p = Backend.providerById(selectedProvider);
            if (!p)
                return null;
            for (let j = 0; j < templates.length; j++) {
                if (templates[j].type === p.type && templates[j].multiple === true)
                    return templates[j];
            }
        }
        return null;
    }

    // A template that can be added several times, each instance needing its own name
    // (OpenAI-compatible only).
    readonly property bool multiTemplate: template !== null && template.multiple === true

    readonly property string formTitle: {
        if (selectedProvider === "")
            return "";
        if (editing) {
            const p = Backend.providerById(selectedProvider);
            return p ? p.label : selectedProvider;
        }
        if (String(label).trim() !== "")
            return String(label).trim();
        for (let i = 0; i < templates.length; i++) {
            if (templates[i].id === selectedProvider)
                return templates[i].label;
        }
        return selectedProvider;
    }

    function show() {
        reset();
        visible = true;
    }

    function showFor(providerId) {
        reset();
        const p = Backend.providerById(providerId);
        if (!p)
            return;
        editing = true;
        selectedProvider = providerId;
        label = p.label || "";
        baseUrl = p.baseUrl || "";
        apiKey = "";
        hasStoredKey = p.hasKey === true;
        canNativeSearch = p.canNativeSearch === true;
        nativeSearch = p.nativeSearch !== false;
        modelList = (p.models || []).map(m => ({
            "id": m.id,
            "label": m.label || m.id
        }));
        const map = {};
        (p.models || []).forEach(m => {
            if (m.enabled === true)
                map[m.id] = true;
        });
        enabledMap = map;
        visible = true;
    }

    function hide() {
        visible = false;
    }

    onClosed: hide()

    function reset() {
        selectedProvider = "";
        editing = false;
        label = "";
        baseUrl = "";
        apiKey = "";
        modelList = [];
        enabledMap = ({});
        busy = false;
        errorText = "";
        fetchedNow = false;
        hasStoredKey = false;
        nativeSearch = true;
        canNativeSearch = false;
    }

    function chooseTemplate(template) {
        // A service already added cannot be added again — that would put two identically
        // named models in the picker. Templates with multiple === true are the exception: running
        // a local Ollama and a company vLLM side by side is normal, so the user names them.
        if (Backend.providerById(template.id) !== null && template.multiple !== true)
            return;

        selectedProvider = template.id;
        editing = false;
        label = template.multiple === true ? "" : (template.label || "");
        baseUrl = template.baseUrl || "";
        canNativeSearch = template.type === "openai-responses" || template.type === "gemini" || template.type === "anthropic";
        nativeSearch = true;
        apiKey = "";
        modelList = [];
        enabledMap = ({});
        errorText = "";
        fetchedNow = false;
        hasStoredKey = false;
    }

    // Add flow only: back to the service list. The edit page is entered straight from the
    // provider list and has nothing to go back to.
    function back() {
        reset();
    }

    // How many instances of a template exist, for the "N added" badge.
    //
    // Matched by id rather than type: DeepSeek and OpenRouter are both type "openai", so
    // counting by type would include other providers.
    function addedCountFor(templateId) {
        let n = 0;
        for (let i = 0; i < Backend.providers.length; i++) {
            const id = Backend.providers[i].id || "";
            if (id === templateId || id.indexOf(templateId + "-") === 0)
                n++;
        }
        return n;
    }

    function setEnabled(id, on) {
        const next = Object.assign({}, enabledMap);
        if (on)
            next[id] = true;
        else
            delete next[id];
        enabledMap = next;

        if (editing) {
            const p = Backend.providerById(selectedProvider);
            const exists = p && (p.models || []).some(m => m.id === id);
            if (exists)
                Backend.setProviderModel(selectedProvider, id, on);
        }
    }

    function fetchModels() {
        if (busy)
            return;
        errorText = "";

        const url = String(baseUrl).trim();
        if (url === "") {
            errorText = I18n.tr("Enter the endpoint first.", "provider setup error when the endpoint field is empty");
            return;
        }

        busy = true;

        const apply = models => {
            busy = false;
            fetchedNow = true;
            // Keep the upstream label: the pill shows it large and the id small.
            modelList = (models || []).map(m => ({
                "id": m.id || m,
                "label": m.name || m.label || m.id || m
            }));
        };

        if (editing) {
            // Send only changed fields; an empty apiKey keeps the stored one.
            const current = Backend.providerById(selectedProvider);
            const fields = {};
            if (current && url !== current.baseUrl)
                fields.baseUrl = url;
            if (String(apiKey).trim() !== "")
                fields.apiKey = String(apiKey).trim();
            if (current && nativeSearch !== (current.nativeSearch !== false))
                fields.nativeSearch = nativeSearch;

            const runFetch = () => {
                Backend.fetchProviderModels(selectedProvider, (res, err) => {
                    if (err) {
                        busy = false;
                        errorText = err;
                        return;
                    }
                    const p = Backend.providerById(selectedProvider);
                    apply(p && p.models ? p.models : []);
                });
            };

            if (Object.keys(fields).length === 0) {
                runFetch();
                return;
            }
            Backend.updateProvider(selectedProvider, fields, (res, err) => {
                if (err) {
                    busy = false;
                    errorText = err;
                    return;
                }
                runFetch();
            });
            return;
        }

        Backend.probeProvider(selectedProvider, url, apiKey, (res, err) => {
            if (err) {
                busy = false;
                errorText = err;
                return;
            }
            apply(res && res.models ? res.models : []);
        });
    }

    function save() {
        if (busy)
            return;
        errorText = "";

        const url = String(baseUrl).trim();
        if (url === "") {
            errorText = I18n.tr("Enter the endpoint first.", "provider setup error when the endpoint field is empty");
            return;
        }

        // Only multi-instance templates ask for a name; the rest use the template label.
        const nameField = providerModal.multiTemplate ? String(label).trim() : "";
        // The name is required for multi-instance templates: two "OpenAI-compatible" entries
        // would be indistinguishable in the model picker.
        if (!editing && providerModal.multiTemplate && nameField === "") {
            errorText = I18n.tr("Give this endpoint a name — you can add more than one.", "provider setup error when the name field is empty for a multi-instance provider");
            return;
        }

        busy = true;

        if (editing) {
            const fields = {
                "baseUrl": url
            };
            if (nameField !== "")
                fields.label = nameField;
            if (String(apiKey).trim() !== "")
                fields.apiKey = String(apiKey).trim();

            Backend.updateProvider(selectedProvider, fields, (res, err) => {
                busy = false;
                if (err) {
                    errorText = err;
                    return;
                }
                ToastService.info(I18n.tr("Saved", "toast after saving provider settings"));
                hide();
            });
            return;
        }

        // Labels are stored with the models; they are what the settings page shows.
        const labels = {};
        modelList.forEach(m => {
            labels[m.id] = m.label || m.id;
        });
        const picked = Object.keys(enabledMap).map(id => ({
            "id": id,
            "label": labels[id] || id
        }));

        Backend.addProvider({
            "type": selectedProvider,
            "label": nameField,
            "baseUrl": url,
            "apiKey": String(apiKey).trim(),
            "models": picked,
            "nativeSearch": nativeSearch
        }, (res, err) => {
            busy = false;
            if (err) {
                errorText = err;
                return;
            }
            ToastService.info(I18n.tr("Provider added", "toast after adding a provider"));
            hide();
        });
    }

    // --- window ---
    title: selectedProvider === "" ? I18n.tr("Add provider", "provider add modal window title") : formTitle
    minimumSize: Qt.size(560, 540)
    implicitWidth: Math.max(minimumSize.width, Theme.modalWidth(parentWindow, screen, 640))
    // Fixed 620: the picker and the form share one box, and an overlong model list scrolls the
    // form instead of growing the window.
    // Theme.modalHeight's parentCap (80% of the parent) is not applied on purpose — the
    // settings window is short enough that the cap would collapse this to ~547.
    implicitHeight: Math.max(minimumSize.height, Math.min(screen ? screen.height - 80 : 900, 620))
    // Alpha-aware: this is what the opacity setting changes.
    color: Theme.windowSurface
    surfaceFormat.opaque: false

    // Asks the compositor to blur what shows through, when the window is see-through and the user
    // asked for it. On a compositor without the protocol this is a silent no-op rather than an error,
    // which is why the setting says so instead of being gated on a probe.
    WindowBlur {
        targetWindow: providerModal
        blurEnabled: Theme.translucentWindows
        blurWidth: providerModal.width
        blurHeight: providerModal.height
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
                title: providerModal.title
                onCloseRequested: providerModal.hide()
            }

            // Must come after the header (later declarations sit on top): the whole header is
            // a drag area, so a button declared before it is covered — the cursor stays an arrow
            // and the click drags the window.
            DActionButton {
                anchors.left: parent.left
                anchors.leftMargin: Theme.spacingS
                anchors.verticalCenter: parent.verticalCenter
                // Only the add flow has a previous page; an edit opened from the list has no
                // picker behind it.
                visible: providerModal.selectedProvider !== "" && !providerModal.editing
                iconName: "arrow_back"
                buttonSize: Theme.buttonHeightXXS
                z: 2
                Accessible.name: I18n.tr("Back", "back button in provider setup")
                onClicked: providerModal.back()
            }
        }

        Item {
            width: parent.width
            height: parent.height - headerRow.height
            clip: true

            Loader {
                id: stepLoader

                anchors.fill: parent
                anchors.margins: Theme.spacingL
                sourceComponent: providerModal.selectedProvider === "" ? providerPicker : providerForm
            }
        }
    }

    Shortcut {
        sequence: "Escape"
        enabled: providerModal.visible
        onActivated: providerModal.hide()
    }

    Toast {
        anchors.fill: parent
        z: 102
    }

    FloatingWindowControls {
        id: windowControls

        targetWindow: providerModal
        z: 101
    }

    // --- service picker ---
    //
    // A plain list (same shape as "add search backend"), not a card grid: two-column cards
    // squeeze the description to a line or two.
    Component {
        id: providerPicker

        DFlickable {
            clip: true
            contentWidth: width
            contentHeight: pickerColumn.implicitHeight + Theme.spacingL * 2

            Column {
                id: pickerColumn

                width: parent.width
                spacing: Theme.spacingM

                StyledText {
                    text: I18n.tr("Choose a provider", "heading for provider selection list")
                    font.pixelSize: Theme.fontSizeLarge
                    font.weight: Theme.fontWeightMedium
                    color: Theme.surfaceText
                    width: parent.width
                }

                StyledText {
                    text: I18n.tr("Keys are stored in the config file on this machine.", "subtitle under provider selection heading")
                    font.pixelSize: Theme.fontSizeMedium
                    color: Theme.surfaceVariantText
                    width: parent.width
                }

                Repeater {
                    model: ScriptModel {
                        values: providerModal.templates
                    }

                    delegate: StyledRect {
                        id: templateRow

                        required property var modelData

                        // An added service is not listed twice, except for multi-instance
                        // templates, which must stay addable.
                        readonly property bool alreadyAdded: Backend.providerById(modelData.id) !== null && modelData.multiple !== true
                        // Multi-instance rows stay addable, so they report how many exist
                        // instead of a checkmark.
                        readonly property int addedCount: modelData.multiple === true ? providerModal.addedCountFor(modelData.id) : (alreadyAdded ? 1 : 0)

                        width: parent.width
                        height: rowColumn.implicitHeight + Theme.spacingM * 2
                        color: rowHover.hovered && !alreadyAdded ? Theme.surfaceContainerHigh : Theme.surfaceContainerLow
                        radius: Theme.cornerRadiusL

                        Behavior on color {
                            ColorAnimation {
                                duration: Theme.expressiveDurations.expressiveFastSpatial
                                easing.type: Easing.BezierSpline
                                easing.bezierCurve: Theme.expressiveCurves.standard
                            }
                        }

                        Row {
                            id: rowColumn

                            anchors.left: parent.left
                            anchors.right: parent.right
                            anchors.verticalCenter: parent.verticalCenter
                            anchors.leftMargin: Theme.spacingM
                            anchors.rightMargin: Theme.spacingM
                            spacing: Theme.spacingM

                            IconBadge {
                                anchors.verticalCenter: parent.verticalCenter
                                iconName: templateRow.modelData.id === "custom" ? "terminal" : "cloud"
                            }

                            Column {
                                width: parent.width - parent.spacing - Theme.iconButtonSize - (addedRow.visible ? addedRow.width + parent.spacing : 0)
                                anchors.verticalCenter: parent.verticalCenter
                                spacing: Theme.spacingXS

                                StyledText {
                                    text: templateRow.modelData.label
                                    font.pixelSize: Theme.fontSizeMedium
                                    font.weight: Theme.fontWeightMedium
                                    color: templateRow.alreadyAdded ? Theme.surfaceVariantText : Theme.surfaceText
                                    width: parent.width
                                    elide: Text.ElideRight
                                }

                                StyledText {
                                    text: I18n.tr(templateRow.modelData.description, "provider template description")
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
                                visible: templateRow.addedCount > 0

                                DIcon {
                                    anchors.verticalCenter: parent.verticalCenter
                                    name: "check"
                                    size: Theme.iconSizeSmall
                                    color: Theme.primary
                                }

                                StyledText {
                                    anchors.verticalCenter: parent.verticalCenter
                                    text: templateRow.addedCount > 1 ? I18n.tr("%1 added", "template row badge with how many instances already exist").arg(templateRow.addedCount) : I18n.tr("Added", "template row badge when the provider already exists")
                                    font.pixelSize: Theme.fontSizeSmall
                                    color: Theme.primary
                                }
                            }
                        }

                        HoverHandler {
                            id: rowHover
                            cursorShape: templateRow.alreadyAdded ? Qt.ArrowCursor : Qt.PointingHandCursor
                        }

                        StateLayer {
                            stateColor: Theme.surfaceText
                            disabled: templateRow.alreadyAdded
                            enabled: !templateRow.alreadyAdded
                            cornerRadius: templateRow.radius
                            onClicked: providerModal.chooseTemplate(templateRow.modelData)
                        }
                    }
                }
            }
        }
    }

    // --- endpoint, key, models ---
    Component {
        id: providerForm

        DFlickable {
            id: formRoot

            clip: true
            contentWidth: width
            contentHeight: formColumn.implicitHeight + Theme.spacingL * 2

            Column {
                id: formColumn

                width: parent.width
                spacing: Theme.spacingM

                StyledText {
                    text: providerModal.editing ? I18n.tr("Change the endpoint, replace the key, or fetch the model list again.", "provider setup instructions when editing") : I18n.tr("Paste an API key, then fetch the model list. Turn on the models you want in the picker.", "provider setup instructions when adding")
                    font.pixelSize: Theme.fontSizeMedium
                    color: Theme.surfaceText
                    width: parent.width
                    wrapMode: Text.WordWrap
                }

                // Only multi-instance templates ask for a name; the rest use the template
                // label and one more field would just be one more thing to fill in.
                DTextField {
                    width: parent.width
                    visible: providerModal.multiTemplate
                    outlined: true
                    labelText: I18n.tr("Name", "provider setup name field label")
                    placeholderText: I18n.tr("e.g. Ollama on this machine", "provider setup name field placeholder")
                    text: providerModal.label
                    onTextChanged: providerModal.label = text
                }

                DTextField {
                    width: parent.width
                    outlined: true
                    labelText: I18n.tr("API endpoint", "provider setup endpoint field label")
                    placeholderText: "https://api.example.com/v1"
                    leftIconName: "cloud"
                    text: providerModal.baseUrl
                    onTextChanged: providerModal.baseUrl = text
                    Component.onCompleted: forceActiveFocus()
                }

                DTextField {
                    width: parent.width
                    outlined: true
                    labelText: providerModal.hasStoredKey ? I18n.tr("API key — leave empty to keep the saved one", "provider setup key label when a key is stored") : I18n.tr("API key", "provider setup key field label")
                    placeholderText: providerModal.hasStoredKey ? "••••••••" : "sk-…"
                    leftIconName: "key"
                    echoMode: TextInput.Password
                    showPasswordToggle: true
                    text: providerModal.apiKey
                    onTextChanged: providerModal.apiKey = text
                }

                // Only native APIs have built-in search; showing this for compatible
                // endpoints would imply they do.
                SettingsToggleRow {
                    width: parent.width
                    visible: providerModal.canNativeSearch
                    text: I18n.tr("Use the model's built-in search", "provider setup native search toggle")
                    description: I18n.tr("The provider searches on its own side, so the local web_search tool is not offered. This is billed per search.", "provider setup native search toggle description")
                    checked: providerModal.nativeSearch
                    onToggled: checked => providerModal.nativeSearch = checked
                }

                Row {
                    width: parent.width
                    spacing: Theme.spacingM

                    DButton {
                        text: providerModal.busy ? I18n.tr("Fetching…", "fetch button while loading") : (providerModal.modelList.length > 0 ? I18n.tr("Refresh models", "fetch button when a list is already shown") : I18n.tr("Fetch models", "fetch button"))
                        iconName: "refresh"
                        enabled: !providerModal.busy
                        variant: "tonal"
                        onClicked: providerModal.fetchModels()
                    }

                    StyledText {
                        anchors.verticalCenter: parent.verticalCenter
                        text: {
                            if (providerModal.modelList.length === 0)
                                return "";
                            if (providerModal.fetchedNow)
                                return I18n.tr("%1 models found", "model count after fetching").arg(providerModal.modelList.length);
                            return I18n.tr("%1 models", "model count when the list came from settings").arg(providerModal.modelList.length);
                        }
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                    }
                }

                // The model list scrolls with the page — no nested Flickable.
                //
                // Every model is its own pill rather than a SettingsGroup chain: grouped, only the
                // first and last get rounded corners and the middles look like one block chopped
                // up, which reads as anything but per-item switches.
                Column {
                    width: parent.width
                    spacing: Theme.groupedListGap
                    visible: providerModal.modelList.length > 0

                    StyledText {
                        text: I18n.tr("Models", "provider setup model list heading")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                        width: parent.width
                    }

                    Repeater {
                        model: ScriptModel {
                            values: providerModal.modelList
                        }

                        // The wrapping Item is deliberate: SettingsRow picks its corner radius
                        // from whether siblings exist, so wrapping makes every model a lone card.
                        delegate: Item {
                            id: modelItem

                            required property var modelData

                            width: parent.width
                            height: modelPill.implicitHeight

                            SettingsToggleRow {
                                id: modelPill

                                width: parent.width
                                // The large line is the upstream label (as OpenRouter sends);
                                // the id only shows when the two differ.
                                text: modelData.label || modelData.id
                                description: modelData.label && modelData.label !== modelData.id ? modelData.id : ""
                                checked: providerModal.enabledMap[modelData.id] === true
                                onToggled: checked => providerModal.setEnabled(modelData.id, checked)
                            }
                        }
                    }
                }

                StyledText {
                    visible: providerModal.errorText !== ""
                    text: providerModal.errorText
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.error
                    width: parent.width
                    wrapMode: Text.WordWrap
                }

                DButton {
                    anchors.right: parent.right
                    text: providerModal.editing ? I18n.tr("Save", "save button when editing a provider") : I18n.tr("Add provider", "save button when adding a provider")
                    iconName: "check"
                    enabled: !providerModal.busy
                    onClicked: providerModal.save()
                }
            }
        }
    }
}
