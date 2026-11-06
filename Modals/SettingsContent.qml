import QtQuick
import QtQuick.Controls
import Quickshell
import qs.Common
import qs.DCommon.FileBrowser
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets
import qs.DCommon.Common as DC

Item {
    id: root

    property int currentIndex: 0
    property var hostWindow: null

    readonly property var colorSourceOptions: [
        {
            label: I18n.tr("Auto", "color source button group option following DMS"),
            value: "auto"
        },
        {
            label: I18n.tr("Matugen", "color source button group option for Matugen"),
            value: "matugen"
        },
        {
            label: I18n.tr("Preset", "color source button group option for bundled palettes"),
            value: "preset"
        },
        {
            label: I18n.tr("Custom", "color source button group option for a custom theme file"),
            value: "custom"
        }
    ]

    // Three sources, same as dux-calendar and dux-files: follow Dux Shell, follow Qt's own system
    // font, or pick a family here.
    //
    // "System" is not "the three families from the desktop": FontSource maps it to Fonts.system for
    // the proportional font, Fonts.systemMono ("monospace") for code, and the bundled DM Serif for
    // display — so what it really changes is the body font. The family dropdowns are hidden under it
    // for that reason rather than offered and ignored.
    // Ordered from the most room to the least, so the buttons read as a scale.
    readonly property var densityOptions: [
        {
            label: I18n.tr("Spacious", "interface density option with the most room between items"),
            value: "spacious"
        },
        {
            label: I18n.tr("Comfortable", "interface density option with the default spacing"),
            value: "comfortable"
        },
        {
            label: I18n.tr("Compact", "interface density option fitting the most items on screen"),
            value: "compact"
        },
        {
            label: I18n.tr("Dense", "interface density option denser than compact for small text and tight rows"),
            value: "dense"
        }
    ]

    readonly property var fontSourceOptions: [
        {
            label: I18n.tr("Auto", "font source button group option following DMS"),
            value: "auto"
        },
        {
            label: I18n.tr("System", "font source button group option for the Qt system font"),
            value: "system"
        },
        {
            label: I18n.tr("Custom", "font source button group option for app-chosen fonts"),
            value: "custom"
        }
    ]

    function localeDisplayName(code) {
        const name = I18n.presentLocales[code].nativeLanguageName;
        return name.charAt(0).toUpperCase() + name.slice(1);
    }

    readonly property var bundledLocaleOptions: Object.keys(I18n.presentLocales).sort().map(code => ({
                label: root.localeDisplayName(code),
                value: code
            }))

    readonly property var languageOptions: [
        {
            label: I18n.tr("System default", "language dropdown option that follows the system language"),
            value: ""
        }
    ].concat(bundledLocaleOptions)

    function minutesFromClock(text) {
        const parts = (text || "").split(":");
        if (parts.length !== 2)
            return 540;
        const total = parseInt(parts[0], 10) * 60 + parseInt(parts[1], 10);
        return isNaN(total) ? 540 : total;
    }

    function clockFromMinutes(minutes) {
        const h = Math.floor(minutes / 60);
        const m = minutes % 60;
        return (h < 10 ? "0" : "") + h + ":" + (m < 10 ? "0" : "") + m;
    }

    function optionLabels(options) {
        return options.map(o => o.label);
    }

    function labelForValue(options, value) {
        for (let i = 0; i < options.length; i++) {
            if (options[i].value === value)
                return options[i].label;
        }
        return options[0].label;
    }

    // Token counts as short labels, 1024-based to match the dropdowns (131072 reads as 128k), so a
    // model's limit and the option chosen next to it speak the same units.
    function sizeLabel(v) {
        if (v >= 1048576)
            return (v % 1048576 === 0 ? v / 1048576 : (v / 1048576).toFixed(1)) + "M";
        if (v >= 1024)
            return Math.round(v / 1024) + "k";
        return String(v);
    }

    function valueForLabel(options, label) {
        for (let i = 0; i < options.length; i++) {
            if (options[i].label === label)
                return options[i].value;
        }
        return options[0].value;
    }

    // Output cap options; values are tokens, labels use 1024-based shorthand (32k / 256k).
    // 0 means "off": the cap is left to the provider. The top entry is deliberately far above any
    // reply actually written — real ones measure a few thousand tokens — because the cost of being
    // wrong is a reply cut mid-sentence, and newer models think longer than this one.
    //
    // A value outside the list (hand-edited config.json) must still display, or the dropdown
    // silently pretends it is off.
    readonly property var outputLimitValues: [0, 32768, 65536, 131072, 262144]

    function outputLimitLabel(v) {
        return v === 0 ? I18n.tr("Off", "max output tokens option, meaning no cap") : Math.round(v / 1024) + "k";
    }

    // The selected model's own output ceiling, 0 when its provider does not report one. The daemon
    // lowers the cap to this instead of sending a number the API would reject, so the row has to say
    // so: a dropdown reading 256k next to a model that takes 16k would otherwise be a lie.
    readonly property int modelOutputCeiling: Models.current && Models.current.maxOutput ? Models.current.maxOutput : 0

    readonly property string outputCapDescription: {
        const base = I18n.tr("A hard cap: anything past it is cut mid-sentence and marked truncated.", "max output tokens dropdown description");
        if (modelOutputCeiling <= 0)
            return base;
        if (Backend.maxOutputTokens > modelOutputCeiling)
            return base + " " + I18n.tr("This model takes at most %1 tokens, so that is what gets sent.", "max output tokens dropdown description, cap above the model ceiling").arg(root.sizeLabel(modelOutputCeiling));
        return base + " " + I18n.tr("This model takes at most %1 tokens.", "max output tokens dropdown description, model ceiling").arg(root.sizeLabel(modelOutputCeiling));
    }

    readonly property var outputLimitOptions: {
        const list = outputLimitValues.map(function (v) {
            return {
                "label": outputLimitLabel(v),
                "value": v
            };
        });
        const cur = Backend.maxOutputTokens;
        if (outputLimitValues.indexOf(cur) === -1)
            list.push({
                "label": outputLimitLabel(cur),
                "value": cur
            });
        return list;
    }

    // Compaction threshold options. 0 turns it off; the rest are estimated token counts.
    // No "auto" default: a guess that clever only makes behaviour harder to predict.
    //
    // The top entries sit above the context window of most current models on purpose: compacting is
    // what makes a long turn lose the beginning of its own history, so the last thing wanted is a
    // threshold that fires during an ordinary afternoon.
    readonly property var compactValues: [0, 65536, 131072, 262144, 524288]

    readonly property var compactOptions: {
        const cur = Backend.compactAt;
        const list = compactValues.map(function (v) {
            return {
                "label": v === 0 ? I18n.tr("Off", "context compaction disabled option") : (v / 1024) + "k",
                "value": v
            };
        });
        if (compactValues.indexOf(cur) === -1)
            list.push({
                "label": cur === 0 ? "0" : (cur / 1024) + "k",
                "value": cur
            });
        return list;
    }

    // The daemon lowers a threshold that does not fit the model's window, because past the window the
    // request is rejected rather than compacted. Saying so is the difference between "compact less" and
    // "this number cannot take effect"; the comparison comes from the daemon so there is one rule, not
    // two copies of it.
    readonly property string compactDescription: {
        const base = I18n.tr("Older history becomes a summary past this size. The messages it replaces stay in the transcript, folded under the summary.", "context compaction threshold dropdown description");
        if (Backend.compactAt <= 0 || Backend.effectiveCompactAt <= 0 || Backend.effectiveCompactAt >= Backend.compactAt)
            return base;
        return base + " " + I18n.tr("This model cannot read that much, so compaction actually happens at %1.", "context compaction threshold dropdown description, threshold lowered to fit the model's window").arg(root.sizeLabel(Backend.effectiveCompactAt));
    }

    // Step cap per turn; 0 turns it off. It stops the model going in circles rather than limiting
    // what the user can do, so the default is generous and the top entry is past any real turn: one
    // measured session used 116 steps in a single turn without circling at all.
    readonly property var toolStepValues: [0, 50, 100, 200, 300]

    function toolStepLabel(v) {
        return v === 0 ? I18n.tr("Off", "max tool steps option, meaning no cap") : String(v);
    }

    readonly property var toolStepOptions: {
        const list = toolStepValues.map(function (v) {
            return {
                "label": toolStepLabel(v),
                "value": v
            };
        });
        // Out-of-list values (hand-edited config.json) must display too, or the dropdown
        // pretends it is the default and the next click overwrites the real setting.
        const cur = Backend.maxToolSteps;
        if (toolStepValues.indexOf(cur) === -1)
            list.push({
                "label": toolStepLabel(cur),
                "value": cur
            });
        return list;
    }

    // Three-way: two independent switches would allow "neither expand nor collapse", which
    // nobody wants and everybody has to reason about. Labels are pills, so they stay to a word or
    // two; what "auto" means lives in the description below.
    readonly property var thinkingModeOptions: [
        {
            "label": I18n.tr("Auto", "display mode option: auto for the thinking block"),
            "value": "auto"
        },
        {
            "label": I18n.tr("On", "display mode option: thinking block always expanded"),
            "value": "open"
        },
        {
            "label": I18n.tr("Off", "display mode option: thinking block always collapsed"),
            "value": "closed"
        }
    ]

    readonly property var toolModeOptions: [
        {
            "label": I18n.tr("Auto", "display mode option: auto for tool cards"),
            "value": "auto"
        },
        {
            "label": I18n.tr("On", "display mode option: tool cards always expanded"),
            "value": "open"
        },
        {
            "label": I18n.tr("Off", "display mode option: tool cards always collapsed"),
            "value": "closed"
        }
    ]

    // Approval policy. The middle option asks only when it matters: writes cannot leave the
    // working directory anyway (Resolve stops them), and commands are judged by whether a path
    // outside it appears literally. Ordered by risk — ask every time → inside the working
    // directory → never ask — so it loosens left to right and the most permissive is not first.
    readonly property var approvalModeOptions: [
        {
            "label": I18n.tr("Off", "approval mode option: always ask"),
            "value": "ask"
        },
        {
            "label": I18n.tr("Inside the working directory", "approval mode option: auto approve inside the working directory"),
            "value": "workspace"
        },
        {
            "label": I18n.tr("On", "approval mode option: approve everything"),
            "value": "auto"
        }
    ]

    readonly property var pageTitles: [I18n.tr("General", "general settings section header"), I18n.tr("Appearance", "appearance settings section header"), I18n.tr("Providers", "providers settings section header"), I18n.tr("Extensions", "extensions settings section header"), I18n.tr("Web search", "web search settings section header"), I18n.tr("Notifications", "notifications settings section header"), I18n.tr("About", "settings sidebar tab label")]

    // Same order as pageTitles and the sidebar; the search walks these.
    readonly property var pageComponents: [generalPage, appearancePage, providersPage, extensionsPage, searchPage, notificationsPage, aboutPage]

    // ---------- search ----------

    property string searchQuery: ""
    readonly property bool searching: searchQuery.trim() !== ""
    // { title, where, page, section, row }
    property var searchResults: []
    // Hits per page, shown beside each sidebar tab so the query says where the answers are.
    property var searchCounts: []

    function _collectRows(item, out, state) {
        for (const child of item.children) {
            if (!child.visible)
                continue;
            if (child.isSettingsCard === true)
                state.section = child.title;
            if (child.isSettingsRow === true) {
                if (child.title !== "")
                    out.push({
                        "row": child,
                        "section": state.section
                    });
                continue;
            }
            _collectRows(child, out, state);
        }
        return out;
    }

    // Ranks every visible row of every page against the query.
    //
    // Nothing is stored: the index *is* the widget tree, read on each keystroke. A settings page is a
    // few dozen rows, so there is nothing to cache and no second copy of the titles to keep in sync.
    function _rebuildSearch(attempt) {
        if (!searching) {
            searchResults = [];
            searchCounts = [];
            return;
        }
        attempt = attempt || 0;
        const found = [];
        const counts = [];
        // Rows carry translated text, so the English side of the match comes from I18n's reverse map.
        // Before the locale has loaded there is nothing to look up yet, and the results would stay
        // empty for a query the user has already finished typing.
        let waiting = !I18n.translationsLoaded;
        for (let page = 0; page < pageHost.count; page++) {
            counts.push(0);
            const loader = pageHost.itemAt(page);
            const item = loader ? loader.item : null;
            if (!item) {
                waiting = true;
                continue;
            }
            for (const hit of _collectRows(item, [], {
                "section": ""
            })) {
                const row = hit.row;
                // Both languages: everything on the row is already translated by the time we read it,
                // so an English query has to be matched against the terms the text came from. That
                // includes `keywords`, which is one translated phrase of synonyms — without the
                // reverse lookup the English spelling of a synonym would be unfindable.
                const score = Math.max(SettingsSearch.score(searchQuery, row.title, row.subtitle, row.keywords),
                                       SettingsSearch.score(searchQuery, I18n.englishFor(row.title), I18n.englishFor(row.subtitle), I18n.englishFor(row.keywords)));
                if (score <= 0)
                    continue;
                counts[page]++;
                found.push({
                    "title": row.title,
                    "detail": row.subtitle !== "" ? row.subtitle : hit.section,
                    "where": [pageTitles[page], hit.section].filter(part => part !== "").join(" · "),
                    "page": page,
                    "score": score,
                    "order": found.length,
                    "row": row
                });
            }
        }
        found.sort((a, b) => b.score - a.score || a.order - b.order);
        searchResults = found;
        searchCounts = counts;

        // A page still loading has no widgets to read. Asking again on the next event-loop turns is
        // enough — a settings page is instantiated in one go — and bounded, so a page that fails to
        // build cannot spin here.
        if (waiting && attempt < 40)
            Qt.callLater(function () { root._rebuildSearch(attempt + 1); });
    }

    onSearchQueryChanged: Qt.callLater(root._rebuildSearch)

    // A query typed before the locale finished loading has to be answered again once it has.
    Connections {
        target: I18n

        function onTranslationsLoadedChanged() {
            if (root.searching)
                Qt.callLater(root._rebuildSearch);
        }
    }


    // Enter in the search field opens the best match rather than making the user click it.
    function pickFirst() {
        if (searchResults.length > 0)
            _revealHit(searchResults[0]);
    }

    // The field lives in the sidebar and the current page in the window, both owned from outside this
    // item — and a property that is bound from above cannot be assigned from below. So picking a hit
    // asks rather than sets.
    signal searchDismissed
    signal pageRequested(int page)

    // Jumping to a hit: switch page, then reveal the row once that page has built.
    function _revealHit(hit) {
        root.pageRequested(hit.page);
        root.searchDismissed();
        revealTimer.hit = hit;
        revealTimer.restart();
    }

    readonly property Timer revealTimer: Timer {
        id: revealTimer
        property var hit: null
        interval: 60
        repeat: true
        onTriggered: {
            if (!hit)
                return stop();
            const loader = pageHost.itemAt(hit.page);
            if (!loader || !loader.item)
                return;
            stop();
            pageFlickableFor(loader.item)?.revealRow(hit.row);
            hit = null;
        }
    }

    function pageFlickableFor(item) {
        return item && item.revealRow !== undefined ? item : null;
    }

    // Results take the page area over while a query is typed, in the same page frame the settings
    // themselves use — so they scroll, pad and cap their width identically.
    Item {
        id: resultsHost

        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: pageHeader.bottom
        anchors.bottom: parent.bottom
        visible: root.searching
        z: 10

        SettingsPage {
            id: resultsPage

            property int selected: -1

            function focusFirst() {
                selected = 0;
                forceFocus.start();
            }

            readonly property Timer forceFocus: Timer {
                interval: 1
                onTriggered: resultsPage.forceActiveFocus()
            }

            function openSelected() {
                if (selected >= 0 && selected < root.searchResults.length)
                    root._revealHit(root.searchResults[selected]);
            }

            Keys.onPressed: event => {
                if (event.key === Qt.Key_Down) {
                    selected = Math.min(root.searchResults.length - 1, selected + 1);
                    event.accepted = true;
                } else if (event.key === Qt.Key_Up) {
                    selected = Math.max(0, selected - 1);
                    event.accepted = true;
                } else if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) {
                    openSelected();
                    event.accepted = true;
                }
            }

            StyledText {
                width: parent.width
                visible: root.searchResults.length === 0
                text: I18n.tr("Nothing matches \"%1\".", "settings search empty state").arg(root.searchQuery)
                font.pixelSize: Theme.fontSizeMedium
                color: Theme.surfaceVariantText
                horizontalAlignment: Text.AlignLeft
                wrapMode: Text.WordWrap
            }

            Repeater {
                model: root.searchResults

                SettingsRow {
                    required property var modelData
                    required property int index

                    width: parent.width
                    title: modelData.title
                    subtitle: modelData.detail
                    clickable: true
                    highlighted: resultsPage.selected === index
                    trailingBadge: modelData.where
                    showChevron: true
                    onClicked: root._revealHit(modelData)
                }
            }
        }
    }

    Item {
        id: pageHeader
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: parent.top
        height: Math.max(SettingsMetrics.pageHeaderHeight, pageHeading.implicitHeight + Theme.spacingM * 2)

        StyledText {
            id: pageHeading
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.verticalCenter: parent.verticalCenter
            text: root.searching ? I18n.tr("Search results", "settings page header while a search query is typed") : (root.pageTitles[root.currentIndex] ?? "")
            font.pixelSize: Theme.fontSizeXXLarge
            color: Theme.surfaceText
            wrapMode: Text.WordWrap
        }
    }

    // Pages sit under the header, and under the search field while searching.
    //
    // One Loader per page, all kept alive while searching: the search walks every page's widgets, so
    // they all have to exist. That is affordable because their setup is either idle or idempotent (the
    // extensions page refetches skills and MCP status; both are reads). Outside a search exactly one
    // page is loaded, as before — a settings window should not pay for every page to open.
    Item {
        id: pageArea

        anchors.left: parent.left
        anchors.right: parent.right
        anchors.top: pageHeader.bottom
        anchors.bottom: parent.bottom
        clip: true
        // Faded rather than hidden: the search reads each page's *effective* visibility to decide
        // which rows apply right now (the font family rows disappear under "system"), and a hidden
        // parent makes that read false for every row underneath it.
        opacity: root.searching ? 0 : 1

        Repeater {
            id: pageHost

            model: root.pageComponents.length

            Loader {
                id: pageLoader

                required property int index
                readonly property bool current: root.currentIndex === index

                // Off-screen instead of invisible, for the same reason: a page has to stay
                // "visible" for the search to walk it, and it must not be painted.
                width: pageArea.width
                height: pageArea.height
                y: current ? 0 : pageArea.height
                active: current || root.searching
                sourceComponent: root.pageComponents[index]
                // A page instantiated for the search has no rows until it loads; without this the
                // rebuild runs against half-built pages and the results come out empty.
                // The pages for a search are instantiated on demand, so the first rebuild runs
                // against half-built pages. _rebuildSearch asks again until they are all in.
                
            }
        }
    }

    // Provider setup is its own FloatingWindow (like dux-calendar's AccountAddModal), so it
    // survives tab switches and can be dragged elsewhere.
    ProviderAddModal {
        id: providerAdd

        parentWindow: root.hostWindow
    }

    // Search backends get their own window too: same flow, far fewer fields (type + URL or key).
    SearchProviderModal {
        id: searchAdd

        parentWindow: root.hostWindow
    }

    function openProviderSetup(providerId) {
        root.currentIndex = 2;
        if (providerId && providerId !== "")
            providerAdd.showFor(providerId);
        else
            providerAdd.show();
    }

    component PageHeader: Column {
        property string subtitle: ""

        width: parent.width
        visible: subtitle !== ""

        StyledText {
            text: parent.subtitle
            font.pixelSize: Theme.fontSizeMedium
            color: Theme.surfaceVariantText
            width: parent.width
            wrapMode: Text.WordWrap
            horizontalAlignment: Text.AlignLeft
        }
    }

    component FontFamilyRow: SettingsDropdownRow {
        property string family: ""
        property string defaultFamily: Theme.defaultFontFamily
        readonly property string defaultLabel: I18n.tr("Default", "font family dropdown option for the bundled font")

        signal familyPicked(string name)

        enableFuzzySearch: true
        options: [defaultLabel]
        currentValue: family || defaultLabel
        onValueChanged: value => familyPicked(value === defaultLabel ? "" : value)
        Component.onCompleted: Qt.callLater(() => options = [defaultLabel].concat(DC.Fonts.selectableFamilies(defaultFamily)))
    }

    // Still used for the interface language, output cap and compaction threshold: numbers or
    // open-ended lists that do not fit in pills.
    component OptionDropdownRow: SettingsDropdownRow {
        id: optionRow
        property var optionList: []
        property var current: null

        signal picked(var value)

        options: root.optionLabels(optionList)
        currentValue: root.labelForValue(optionList, current)
        // onValueChanged also fires when the currentValue binding updates, i.e. when a value
        // arrives asynchronously from the daemon. Writing back then would overwrite the real
        // setting with a default that had not loaded yet, so only a user-driven change is saved.
        onValueChanged: label => {
            if (label === root.labelForValue(optionList, current))
                return;
            picked(root.valueForLabel(optionList, label));
        }
    }

    Component {
        id: generalPage
        SettingsPage {
            PageHeader {
                subtitle: I18n.tr("Application behavior and agent defaults.", "general settings section subtitle")
            }

            SettingsCard {

                title: I18n.tr("App", "general settings section label")
                SettingsToggleRow {
                    text: I18n.tr("Start at login", "autostart setting label")
                    keywords: I18n.tr("autostart boot startup", "settings search keywords, space separated, add synonyms in your language")
                    description: Backend.connected ? I18n.tr("Launch Dux Code in the background when you log in.", "autostart setting description") : I18n.tr("Available once the daemon is running.", "autostart setting description when the backend is offline")
                    checked: Backend.autostartEnabled
                    enabled: Backend.connected
                    onToggled: checked => Backend.setAutostart(checked)
                }

                SettingsButtonGroupRow {
                    text: I18n.tr("Close behavior", "close behavior setting label")
                    keywords: I18n.tr("quit exit hide tray", "settings search keywords, space separated, add synonyms in your language")
                    description: I18n.tr("What the window's close button does.", "close behavior setting description")
                    model: [I18n.tr("Minimize", "close behavior button group option to hide to tray"), I18n.tr("Quit", "close behavior button group option to quit the app")]
                    currentIndex: SettingsData.closeBehavior === "quit" ? 1 : 0
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        SettingsData.closeBehavior = index === 1 ? "quit" : "minimize";
                    }
                }

                SettingsToggleRow {
                    text: I18n.tr("Show tray icon", "tray icon toggle label")
                    keywords: I18n.tr("tray indicator", "settings search keywords, space separated, add synonyms in your language")
                    description: !SettingsData.showTrayIcon && SettingsData.closeBehavior === "minimize" ? I18n.tr("Closing hides the window; reopen it from your app launcher.", "tray icon toggle warning when closing only minimizes") : I18n.tr("Show Dux Code in the system tray.", "tray icon toggle description")
                    checked: SettingsData.showTrayIcon
                    onToggled: checked => SettingsData.showTrayIcon = checked
                }
            }

            SettingsCard {

                title: I18n.tr("Language", "general settings section label")
                OptionDropdownRow {
                    text: I18n.tr("Language", "interface language setting label")
                    description: I18n.tr("Language of the interface.", "interface language setting description")
                    dropdownWidth: 220
                    optionList: root.languageOptions
                    current: SettingsData.language
                    onPicked: value => SettingsData.language = value
                }
            }

            SettingsCard {

                title: I18n.tr("Agent", "general settings section label")
                // Three fixed options as pills, matching the font source row; dropdowns are for
                // open-ended or long lists (model, reasoning depth, font family).
                SettingsButtonGroupRow {
                    text: I18n.tr("Auto-expand the thinking block", "thinking display mode button group label")
                    description: I18n.tr("Auto opens the block while reasoning and folds it when the answer starts.", "thinking display mode button group description")
                    model: root.optionLabels(root.thinkingModeOptions)
                    currentIndex: Math.max(0, root.thinkingModeOptions.findIndex(o => o.value === SettingsData.thinkingDisplay))
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        SettingsData.thinkingDisplay = root.thinkingModeOptions[index].value;
                    }
                }

                SettingsButtonGroupRow {
                    text: I18n.tr("Auto-expand tool calls", "tool display mode button group label")
                    description: I18n.tr("Auto opens a card while the tool runs and folds it when it finishes.", "tool display mode button group description")
                    model: root.optionLabels(root.toolModeOptions)
                    currentIndex: Math.max(0, root.toolModeOptions.findIndex(o => o.value === SettingsData.toolDisplay))
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        SettingsData.toolDisplay = root.toolModeOptions[index].value;
                    }
                }

                SettingsButtonGroupRow {
                    text: I18n.tr("Auto-approve tool calls", "approval mode button group label")
                    keywords: I18n.tr("permissions approval sandbox trust", "settings search keywords, space separated, add synonyms in your language")
                    // Without a sandbox that mode is only a path check, so the usual sentence
                    // would be false.
                    description: Backend.sandboxAvailable ? I18n.tr("Whether tool calls need approval. Inside the workspace, shell commands also run sandboxed.", "approval mode dropdown description with sandbox") : I18n.tr("Whether tool calls need approval. No sandbox here, so a command can still write anywhere.", "approval mode dropdown description without sandbox")
                    model: root.optionLabels(root.approvalModeOptions)
                    currentIndex: Math.max(0, root.approvalModeOptions.findIndex(o => o.value === Backend.approvalMode))
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        Backend.setApprovalMode(root.approvalModeOptions[index].value);
                    }
                }
            }

            SettingsCard {

                title: I18n.tr("Context", "general settings section label")
                OptionDropdownRow {
                    text: I18n.tr("Max output per reply", "max output tokens dropdown label")
                    keywords: I18n.tr("tokens max_tokens reply length cap", "settings search keywords, space separated, add synonyms in your language")
                    // Say that it is a hard cap: people lower it expecting shorter answers and
                    // get cut-off ones.
                    description: root.outputCapDescription
                    optionList: root.outputLimitOptions
                    current: Backend.maxOutputTokens
                    onPicked: value => Backend.setMaxOutputTokens(value)
                }

                OptionDropdownRow {
                    text: I18n.tr("Compress history above", "context compaction threshold dropdown label")
                    keywords: I18n.tr("compaction summary context threshold", "settings search keywords, space separated, add synonyms in your language")
                    // Say that it concerns what is sent, not billing: this is easy to confuse with
                    // the cumulative token count shown above.
                    description: root.compactDescription
                    optionList: root.compactOptions
                    current: Backend.compactAt
                    onPicked: value => Backend.setCompactAt(value)
                }
            }
        }
    }

    // --- Providers tab value helpers ---
    // Dropdowns only speak labels, so these convert between label and id.
    function modelLabelById(id) {
        const list = Backend.models;
        for (let i = 0; i < list.length; i++) {
            if (list[i].id === id)
                return list[i].label || list[i].id;
        }
        return id;
    }

    function modelIdByLabel(label) {
        const list = Backend.models;
        for (let i = 0; i < list.length; i++) {
            if ((list[i].label || list[i].id) === label)
                return list[i].id;
        }
        return "";
    }

    readonly property var modelLabels: Backend.models.map(m => m.label || m.id)

    function providerStatus(provider) {
        if (!provider)
            return "";
        if (provider.hasKey !== true)
            return I18n.tr("No key", "provider status badge when no API key is configured");
        if (provider.keySource === "config")
            return I18n.tr("Saved", "provider status badge when the key comes from the config file");
        if (typeof provider.keySource === "string" && provider.keySource.indexOf("env:") === 0)
            return provider.keySource.slice(4);
        return I18n.tr("Local", "provider status badge for a self-hosted endpoint without a key");
    }

    Component {
        id: providersPage
        SettingsPage {
            PageHeader {
                subtitle: I18n.tr("Where the models come from. Keys stay on this machine.", "providers settings section subtitle")
            }

            StyledText {
                visible: Backend.providers.length === 0
                text: I18n.tr("No providers yet. Add one to get started.", "providers page empty state")
                font.pixelSize: Theme.fontSizeMedium
                color: Theme.surfaceVariantText
                width: parent.width
                wrapMode: Text.WordWrap
            }

            SettingsGroup {
                Repeater {
                    model: ScriptModel {
                        values: Backend.providers
                    }

                    SettingsRow {
                        id: providerRow

                        required property var modelData

                        readonly property string keyBadge: {
                            if (modelData.hasKey !== true)
                                return I18n.tr("No key", "provider row badge when no API key is configured");
                            if (modelData.keySource === "config")
                                return I18n.tr("Key set", "provider row badge when the key is stored in the config file");
                            if (typeof modelData.keySource === "string" && modelData.keySource.indexOf("env:") === 0)
                                return modelData.keySource.slice(4);
                            return I18n.tr("Local", "provider row badge for a self-hosted endpoint");
                        }

                        title: modelData.label
                        subtitle: modelData.enabledCount > 0 ? I18n.tr("%1 models enabled", "provider row subtitle listing how many models are on").arg(modelData.enabledCount) : modelData.baseUrl
                        clickable: true
                        showChevron: true
                        trailingBadge: modelData.supported === false ? I18n.tr("Not supported yet", "provider row badge for a provider type the client cannot talk to") : providerRow.keyBadge
                        trailingBadgeColor: (modelData.supported === false || modelData.hasKey !== true) ? Theme.error : Theme.primary
                        onClicked: providerAdd.showFor(modelData.id)

                        leading: IconBadge {
                            anchors.verticalCenter: parent.verticalCenter
                            iconName: providerRow.modelData.id === "custom" || providerRow.modelData.id.indexOf("custom") === 0 ? "terminal" : "cloud"
                        }

                        DActionButton {
                            anchors.verticalCenter: parent.verticalCenter
                            iconName: "delete_outline"
                            iconColor: Theme.error
                            tooltipText: I18n.tr("Remove", "provider row remove button tooltip")
                            onClicked: {
                                providerRemove.target = providerRow.modelData;
                                providerRemove.show({
                                    "title": I18n.tr("Remove \"%1\"?", "remove provider confirmation title").arg(providerRow.modelData.label),
                                    "message": I18n.tr("Its models disappear from the picker. Nothing is deleted on the provider side.", "remove provider confirmation message"),
                                    "confirmText": I18n.tr("Remove", "remove provider confirmation button"),
                                    "danger": true
                                });
                            }
                        }
                    }
                }
            }

            DButton {
                text: I18n.tr("Add provider", "add provider button on providers page")
                iconName: "add"
                onClicked: providerAdd.show()
            }

            ConfirmDialog {
                id: providerRemove

                property var target: null

                onConfirmed: {
                    if (target)
                        Backend.removeProvider(target.id);
                    target = null;
                }
            }
        }
    }

    Component {
        id: appearancePage
        SettingsPage {
            id: appearanceFlickable

            function openThemeFilePicker() {
                // Same as the MCP window: a closed FloatingWindow does not come back on
                // visible=true, so it is rebuilt on every press. The picker keeps no state.
                themePickerLoader.active = false;
                themePickerLoader.active = true;
                const picker = themePickerLoader.item;
                if (!picker)
                    return;
                picker.browserTitle = I18n.tr("Select theme file", "custom theme file picker title");
                picker.open();
            }

            PageHeader {
                subtitle: I18n.tr("Pick a color source, palette, or your own theme file.", "appearance settings section subtitle")
            }

            SettingsCard {

                title: I18n.tr("Theme", "appearance settings section label")
                SettingsButtonGroupRow {
                    text: I18n.tr("Theme", "theme mode setting label")
                    keywords: I18n.tr("dark light mode scheme", "settings search keywords, space separated, add synonyms in your language")
                    description: {
                        switch (SettingsData.themeMode) {
                        case "light":
                            return I18n.tr("Always use the light theme.", "theme mode setting description");
                        case "dark":
                            return I18n.tr("Always use the dark theme.", "theme mode setting description");
                        default:
                            return PortalService.available ? I18n.tr("Following the system color scheme (currently %1).", "theme mode setting description").arg(Theme.isLightMode ? I18n.tr("light", "current scheme name in theme mode description") : I18n.tr("dark", "current scheme name in theme mode description")) : I18n.tr("System preference unavailable — using dark.", "theme mode setting description");
                        }
                    }
                    model: [I18n.tr("Auto", "theme mode button group option"), I18n.tr("Light", "theme mode button group option"), I18n.tr("Dark", "theme mode button group option")]
                    currentIndex: {
                        switch (SettingsData.themeMode) {
                        case "light":
                            return 1;
                        case "dark":
                            return 2;
                        default:
                            return 0;
                        }
                    }
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        switch (index) {
                        case 1:
                            SettingsData.themeMode = "light";
                            break;
                        case 2:
                            SettingsData.themeMode = "dark";
                            break;
                        default:
                            SettingsData.themeMode = "auto";
                            break;
                        }
                    }
                }

                SettingsButtonGroupRow {
                    text: I18n.tr("Color source", "color source setting label")
                    keywords: I18n.tr("palette accent colors", "settings search keywords, space separated, add synonyms in your language")
                    description: {
                        switch (SettingsData.colorSource) {
                        case "preset":
                            return I18n.tr("Use a bundled color palette.", "color source description for preset");
                        case "custom":
                            return I18n.tr("Load colors from your own theme file.", "color source description for custom");
                        case "matugen":
                            return I18n.tr("Follow colors generated by Matugen.", "color source description for Matugen");
                        default:
                            return I18n.tr("Follow Dux Shell colors, falling back to a preset.", "color source description for auto");
                        }
                    }
                    model: root.optionLabels(root.colorSourceOptions)
                    currentIndex: {
                        for (let i = 0; i < root.colorSourceOptions.length; i++) {
                            if (root.colorSourceOptions[i].value === SettingsData.colorSource)
                                return i;
                        }
                        return 0;
                    }
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        SettingsData.colorSource = root.colorSourceOptions[index].value;
                    }
                }

                SettingsRow {
                    visible: SettingsData.colorSource === "auto"
                    iconName: Theme.dmsColorsAvailable ? "check_circle" : "info"
                    iconColor: Theme.dmsColorsAvailable ? Theme.success : Theme.surfaceVariantText
                    title: Theme.dmsColorsAvailable ? I18n.tr("Using Dux Shell dynamic colors.", "auto color source status when DMS is active") : I18n.tr("Dux Shell not detected — using the %1 preset.", "auto color source status when DMS is missing").arg(Theme.presetLabel(SettingsData.presetTheme))
                }

                SettingsRow {
                    visible: SettingsData.colorSource === "matugen"
                    iconName: Theme.customThemeLoaded ? "check_circle" : "info"
                    iconColor: Theme.customThemeLoaded ? Theme.success : Theme.surfaceVariantText
                    title: Theme.customThemeLoaded ? I18n.tr("Using Matugen colors.", "Matugen color source status when loaded") : I18n.tr("Matugen theme missing or invalid — using the %1 preset.", "Matugen color source fallback status").arg(Theme.presetLabel(SettingsData.presetTheme))
                    subtitle: Theme.matugenThemePath
                }

                SettingsRow {
                    visible: SettingsData.colorSource === "preset"
                    title: I18n.tr("Palette: %1", "selected preset palette name").arg(Theme.presetLabel(SettingsData.presetTheme))

                    body: Flow {
                        width: parent.width
                        spacing: Theme.spacingS

                        Repeater {
                            model: Theme.presetNames()

                            Rectangle {
                                id: swatch
                                required property string modelData
                                readonly property bool active: SettingsData.presetTheme === modelData

                                width: Theme.avatarSize
                                height: Theme.avatarSize
                                radius: Theme.fullRadius(width, height)
                                color: Theme.presetColors(modelData).primary
                                border.color: active ? Theme.primary : Theme.outlineVariant
                                border.width: active ? Theme.outlineWidthFocused + Theme.outlineWidth : Theme.outlineWidth
                                scale: active ? 1.1 : 1
                                Accessible.role: Accessible.RadioButton
                                Accessible.name: Theme.presetLabel(modelData)
                                Accessible.checked: active

                                Behavior on scale {
                                    enabled: Theme.animationsEnabled
                                    NumberAnimation {
                                        duration: Theme.shortDuration
                                        easing.type: Theme.emphasizedEasing
                                    }
                                }

                                StateLayer {
                                    stateColor: Theme.surfaceText
                                    cornerRadius: parent.radius
                                    onClicked: SettingsData.presetTheme = swatch.modelData
                                }
                            }
                        }
                    }
                }

                SettingsRow {
                    visible: SettingsData.colorSource === "custom"
                    clickable: true
                    iconName: "folder_open"
                    title: SettingsData.customThemeFile ? SettingsData.customThemeFile.split('/').pop() : I18n.tr("No theme file selected", "custom theme empty state title")
                    subtitle: {
                        if (!SettingsData.customThemeFile)
                            return I18n.tr("Choose a JSON color theme file.", "custom theme hint when none selected");
                        if (Theme.customThemeLoaded)
                            return SettingsData.customThemeFile;
                        return I18n.tr("Could not read that file — expected a JSON color theme.", "custom theme error when file is invalid");
                    }
                    subtitleColor: (SettingsData.customThemeFile && !Theme.customThemeLoaded) ? Theme.error : Theme.surfaceVariantText
                    showChevron: true
                    onClicked: appearanceFlickable.openThemeFilePicker()
                }
            }

            SettingsCard {

                title: I18n.tr("Shape", "appearance settings section label")
                SettingsSliderRow {
                    text: I18n.tr("Radius strength", "global component corner rounding")
                    description: I18n.tr("50 matches Material. Lower is squarer, higher is rounder.", "radius strength slider description")
                    minimumLabel: I18n.tr("Square", "radius strength slider label at zero")
                    value: SettingsData.radiusStrength
                    minimum: 0
                    maximum: 100
                    unit: ""
                    onSliderValueChanged: newValue => SettingsData.radiusStrength = newValue
                }
            }

            SettingsCard {

                title: I18n.tr("Density", "appearance settings section label")
                SettingsButtonGroupRow {
                    text: I18n.tr("Interface density", "interface density setting label")
                    keywords: I18n.tr("spacing compact spacious padding rows", "settings search keywords, space separated, add synonyms in your language")
                    description: I18n.tr("How much room list rows and settings rows get.", "interface density setting description")
                    model: root.optionLabels(root.densityOptions)
                    currentIndex: Math.max(0, root.densityOptions.findIndex(o => o.value === SettingsData.density))
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        SettingsData.density = root.densityOptions[index].value;
                    }
                }
            }

            SettingsCard {

                title: I18n.tr("Zoom", "appearance settings section label")
                SettingsSliderRow {
                    // "Interface scale", not "Scale": the typography card has a font scale, and two
                    // sliders both labelled "Scale" in one page is a coin flip for the reader.
                    text: I18n.tr("Interface scale", "interface zoom slider label")
                    keywords: I18n.tr("zoom ui size", "settings search keywords, space separated, add synonyms in your language")
                    description: SettingsData.uiScaleFromEnv ? I18n.tr("Set by QT_SCALE_FACTOR.", "interface zoom slider description when the environment overrides it") : I18n.tr("Scales the whole app. Takes effect after a restart.", "interface zoom slider description")
                    enabled: !SettingsData.uiScaleFromEnv
                    minimum: 75
                    maximum: 200
                    step: 5
                    unit: "%"
                    value: Math.round((SettingsData.uiScaleFromEnv ? SettingsData.effectiveUiScale : SettingsData.uiScale) * 100)
                    onSliderValueChanged: newValue => SettingsData.uiScale = newValue / 100
                }

                SettingsRow {
                    visible: !SettingsData.uiScaleFromEnv && Math.round(SettingsData.uiScale * 100) !== Math.round(SettingsData.effectiveUiScale * 100)
                    title: I18n.tr("Restart required", "interface zoom pending restart label")
                    subtitle: Backend.connected ? I18n.tr("Dux Code will close and reopen with the new zoom.", "interface zoom pending restart description") : I18n.tr("Backend not connected.", "interface zoom pending restart description when backend is unavailable")

                    DButton {
                        anchors.verticalCenter: parent.verticalCenter
                        text: I18n.tr("Apply and restart", "interface zoom restart button")
                        enabled: Backend.connected
                        variant: "tonal"
                        onClicked: Backend.restart()
                    }
                }
            }

            SettingsCard {

                title: I18n.tr("Typography", "appearance settings section label")
                SettingsButtonGroupRow {
                    text: I18n.tr("Font source", "font source setting label")
                    keywords: I18n.tr("typography family fonts", "settings search keywords, space separated, add synonyms in your language")
                    model: root.optionLabels(root.fontSourceOptions)
                    currentIndex: Math.max(0, root.fontSourceOptions.findIndex(o => o.value === SettingsData.fontSource))
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        SettingsData.fontSource = root.fontSourceOptions[index].value;
                    }
                }

                SettingsRow {
                    visible: SettingsData.fontSource === "auto" && !Theme.typography.followingDms
                    iconName: "info"
                    iconColor: Theme.surfaceVariantText
                    title: I18n.tr("Dux Shell has no font settings to follow, using %1.", "auto font source status when Dux Shell publishes no typography").arg(Theme.fontFamily)
                }

                FontFamilyRow {
                    visible: !Theme.typography.followingDms && SettingsData.fontSource !== "system"
                    text: I18n.tr("Family", "font family dropdown label")
                    family: SettingsData.fontFamily
                    onFamilyPicked: name => SettingsData.fontFamily = name
                }

                FontFamilyRow {
                    visible: !Theme.typography.followingDms && SettingsData.fontSource !== "system"
                    text: I18n.tr("Monospace family", "monospace font family dropdown label")
                    defaultFamily: Theme.defaultMonoFontFamily
                    family: SettingsData.monoFontFamily
                    onFamilyPicked: name => SettingsData.monoFontFamily = name
                }

                FontFamilyRow {
                    visible: !Theme.typography.followingDms && SettingsData.fontSource !== "system"
                    text: I18n.tr("Display family", "display font family dropdown label")
                    defaultFamily: Theme.defaultDisplayFontFamily
                    family: SettingsData.displayFontFamily
                    onFamilyPicked: name => SettingsData.displayFontFamily = name
                }

                SettingsSliderRow {
                    visible: !Theme.typography.followingDms
                    text: I18n.tr("Weight", "font weight slider label")
                    minimum: Font.Thin
                    maximum: Font.Black
                    step: 100
                    showStops: true
                    unit: ""
                    value: SettingsData.fontWeight
                    onSliderValueChanged: newValue => SettingsData.fontWeight = newValue
                }

                SettingsSliderRow {
                    visible: !Theme.typography.followingDms
                    text: I18n.tr("Font scale", "font scale slider label")
                    keywords: I18n.tr("zoom text size", "settings search keywords, space separated, add synonyms in your language")
                    minimum: 75
                    maximum: 150
                    unit: "%"
                    value: Math.round(SettingsData.fontScale * 100)
                    onSliderValueChanged: newValue => SettingsData.fontScale = newValue / 100
                }
            }

            SettingsCard {

                title: I18n.tr("Windows", "appearance settings section label")
                SettingsToggleRow {
                    text: I18n.tr("Title bars", "window title bars toggle label")
                    keywords: I18n.tr("decorations csd header", "settings search keywords, space separated, add synonyms in your language")
                    description: I18n.tr("Off lets the compositor draw window decorations.", "window title bars toggle description")
                    checked: SettingsData.windowTitleBars
                    onToggled: checked => SettingsData.windowTitleBars = checked
                }

                SettingsSliderRow {
                    text: I18n.tr("Opacity", "window opacity slider label")
                    keywords: I18n.tr("transparency translucent glass", "settings search keywords, space separated, add synonyms in your language")
                    description: I18n.tr("Lets the desktop show through the window. Text and controls stay solid.", "window opacity slider description")
                    minimum: 0
                    maximum: 100
                    unit: "%"
                    value: Math.round(SettingsData.floatingWindowTransparency * 100)
                    onSliderValueChanged: newValue => SettingsData.floatingWindowTransparency = newValue / 100
                }

                SettingsToggleRow {
                    text: I18n.tr("Foreground layers", "toggle keeping cards and panes solid over a see-through window")
                    keywords: I18n.tr("cards panes contrast", "settings search keywords, space separated, add synonyms in your language")
                    description: I18n.tr("Keeps cards and panes solid when the window is see-through.", "foreground layers toggle description")
                    checked: SettingsData.floatingWindowForegroundLayers
                    onToggled: checked => SettingsData.floatingWindowForegroundLayers = checked
                }

                SettingsSliderRow {
                    text: I18n.tr("Layer opacity", "foreground layer opacity slider label")
                    enabled: SettingsData.floatingWindowForegroundLayers
                    minimum: 0
                    maximum: 100
                    unit: "%"
                    value: Math.round(SettingsData.floatingWindowForegroundTransparency * 100)
                    onSliderValueChanged: newValue => SettingsData.floatingWindowForegroundTransparency = newValue / 100
                }

                SettingsToggleRow {
                    // Only meaningful for a see-through window: blurring what is fully covered shows
                    // nothing. Greyed out rather than hidden, so the setting is still findable.
                    enabled: Theme.translucentWindows
                    text: I18n.tr("Background blur", "toggle asking the compositor to blur behind see-through windows")
                    keywords: I18n.tr("frosted glass compositor effect", "settings search keywords, space separated, add synonyms in your language")
                    description: SettingsData.floatingWindowTransparency >= 1
                        ? I18n.tr("Needs an opacity below 100%.", "background blur toggle description when the window is opaque")
                        : I18n.tr("Blurs what shows through the window. Needs a compositor that supports background blur.", "background blur toggle description")
                    checked: SettingsData.blurEnabled
                    onToggled: checked => SettingsData.blurEnabled = checked
                }
            }

            SettingsCard {

                title: I18n.tr("Motion", "appearance settings section label")
                SettingsSliderRow {
                    text: I18n.tr("Duration", "animation duration slider label")
                    minimumLabel: I18n.tr("Off", "animation duration slider label at zero")
                    minimum: 0
                    maximum: 1000
                    unit: "ms"
                    value: SettingsData.animationDuration
                    onSliderValueChanged: newValue => SettingsData.animationDuration = newValue
                }

                SettingsButtonGroupRow {
                    text: I18n.tr("Spring", "spring bounce setting label")
                    model: [I18n.tr("Smooth", "spring bounce option"), I18n.tr("Balanced", "spring bounce option"), I18n.tr("Playful", "spring bounce option")]
                    currentIndex: SettingsData.springBounce
                    onSelectionChanged: (index, selected) => {
                        if (!selected)
                            return;
                        SettingsData.springBounce = index;
                    }
                }

                SettingsToggleRow {
                    text: I18n.tr("Ripple effects", "ripple effects toggle label")
                    keywords: I18n.tr("click feedback animation", "settings search keywords, space separated, add synonyms in your language")
                    checked: SettingsData.enableRippleEffects
                    onToggled: checked => SettingsData.enableRippleEffects = checked
                }

                SettingsToggleRow {
                    text: I18n.tr("Reduce motion", "reduce motion toggle label")
                    keywords: I18n.tr("accessibility animation", "settings search keywords, space separated, add synonyms in your language")
                    checked: SettingsData.reduceMotion
                    onToggled: checked => SettingsData.reduceMotion = checked
                }
            }

            SettingsCard {

                title: I18n.tr("Focus ring", "appearance settings section label")
                SettingsToggleRow {
                    text: I18n.tr("Focus ring", "focus ring toggle label")
                    keywords: I18n.tr("outline keyboard accessibility", "settings search keywords, space separated, add synonyms in your language")
                    description: I18n.tr("Outline the control that has keyboard focus.", "focus ring toggle description")
                    checked: SettingsData.focusRingEnabled
                    onToggled: checked => SettingsData.focusRingEnabled = checked
                }

                SettingsSliderRow {
                    text: I18n.tr("Thickness", "focus ring thickness slider label")
                    enabled: SettingsData.focusRingEnabled
                    value: Math.round(SettingsData.focusRingWidth * 10)
                    minimum: 10
                    maximum: 40
                    step: 5
                    decimals: 1
                    unit: "px"
                    onSliderValueChanged: newValue => SettingsData.focusRingWidth = newValue / 10
                }

                SettingsDropdownRow {
                    readonly property var colorOptions: [
                        {
                            label: I18n.tr("Primary", "focus ring color option"),
                            value: "primary",
                            color: Theme.primary
                        },
                        {
                            label: I18n.tr("Secondary", "focus ring color option"),
                            value: "secondary",
                            color: Theme.secondary
                        },
                        {
                            label: I18n.tr("Outline", "focus ring color option"),
                            value: "outline",
                            color: Theme.outline
                        },
                        {
                            label: I18n.tr("Text color", "focus ring color option"),
                            value: "surfaceText",
                            color: Theme.surfaceText
                        }
                    ]

                    text: I18n.tr("Color", "focus ring color setting label")
                    enabled: SettingsData.focusRingEnabled
                    dropdownWidth: 160
                    options: root.optionLabels(colorOptions)
                    optionColorMap: {
                        const map = {};
                        for (const option of colorOptions)
                            map[option.label] = option.color;
                        return map;
                    }
                    currentValue: root.labelForValue(colorOptions, SettingsData.focusRingColor)
                    onValueChanged: value => SettingsData.focusRingColor = root.valueForLabel(colorOptions, value)
                }
            }

            Loader {
                id: themePickerLoader
                active: false
                sourceComponent: FileBrowserModal {
                    parentModal: root.hostWindow
                    bucket: "theme"
                    filters: ["*.json"]
                    onAccepted: paths => SettingsData.customThemeFile = paths[0]
                }
            }
        }
    }


    Component {
        id: notificationsPage
        SettingsPage {
            PageHeader {
                subtitle: I18n.tr("Choose when to receive system notifications and play sounds.", "notifications settings section subtitle")
            }

            SettingsCard {

                title: I18n.tr("System notifications", "notifications settings section label")
                SettingsToggleRow {
                    text: I18n.tr("Agent", "agent notification toggle label")
                    description: I18n.tr("Show a system notification when the agent finishes or needs attention.", "agent notification toggle description")
                    checked: SettingsData.notifyAgentDone
                    onToggled: checked => SettingsData.notifyAgentDone = checked
                }

                SettingsToggleRow {
                    text: I18n.tr("Permissions", "permission notification toggle label")
                    description: I18n.tr("Show a system notification when the agent needs permission.", "permission notification toggle description")
                    checked: SettingsData.notifyAgentPermission
                    onToggled: checked => SettingsData.notifyAgentPermission = checked
                }

                SettingsToggleRow {
                    text: I18n.tr("Errors", "error notification toggle label")
                    description: I18n.tr("Show a system notification when something fails.", "error notification toggle description")
                    checked: SettingsData.notifyAgentError
                    onToggled: checked => SettingsData.notifyAgentError = checked
                }

                SettingsToggleRow {
                    text: I18n.tr("Sound", "notification sound toggle label")
                    description: I18n.tr("Play a sound along with system notifications.", "notification sound toggle description")
                    checked: SettingsData.notificationSounds
                    onToggled: checked => SettingsData.notificationSounds = checked
                }
            }
        }
    }

    Component {
        id: extensionsPage
        SettingsPage {
            id: extPage

            // The list comes from Services/Skills.qml, which the chip beside the input reads too,
            // so a newly installed skill shows up in both.
            Component.onCompleted: {
                Skills.refresh();
                // MCP servers start in the background; refetch on entering the page so a stale
                // "starting" does not stick.
                Backend.refreshConfig();
            }

            // One line of MCP status: disabled / failed / starting / N tools. On failure the tail
            // of stderr comes along — "failed" alone says nothing.
            function mcpStatusText(server) {
                if (server.disabled === true)
                    return I18n.tr("Switched off", "mcp row subtitle when the server is disabled");
                if (server.error)
                    return I18n.tr("Failed to start: %1", "mcp row subtitle when startup failed").arg(String(server.error).slice(0, 160));
                if (server.starting === true)
                    return I18n.tr("Starting…", "mcp row subtitle while the server starts");
                if (server.running === true) {
                    const n = server.toolCount || 0;
                    return n === 1
                        ? I18n.tr("Running · 1 tool", "mcp row subtitle with a single tool")
                        : I18n.tr("Running · %1 tools", "mcp row subtitle with tool count").arg(n);
                }
                return I18n.tr("Stopped", "mcp row subtitle when the server is not running");
            }

            PageHeader {
                subtitle: I18n.tr("Skills, MCP servers and the tools the agent is allowed to use.", "extensions settings section subtitle")
            }

            SettingsCard {

                title: I18n.tr("Available skills", "extensions settings section label")

                // This card only lists; installing and removing happen elsewhere. Tabs swap a
                // Loader's sourceComponent, so the page is rebuilt on every visit and
                // Component.onCompleted is a rescan.

                Repeater {
                    model: Skills.list

                    delegate: SettingsRow {
                        required property var modelData

                        title: modelData.name
                        // A problem comes first: the reason matters more than the description.
                        subtitle: {
                            if (modelData.warning)
                                return modelData.warning;
                            const desc = modelData.description !== "" ? modelData.description : modelData.dir;
                            const n = (modelData.tools || []).length;
                            return n > 0 ? desc + I18n.tr(" · %1 tools", "skills row suffix listing allowed tool count").arg(n) : desc;
                        }
                    }
                }

                SettingsRow {
                    visible: Skills.list.length === 0 && Skills.loaded
                    title: I18n.tr("No skills installed", "skills empty state title")
                    subtitle: Skills.dir !== ""
                              ? I18n.tr("A skill is a folder with a SKILL.md the agent loads on demand. Put it under %1.", "skills empty state description").arg(Skills.dir)
                              : I18n.tr("A skill is a folder with a SKILL.md the agent loads on demand, under ~/.agents/skills.", "skills empty state description")
                }

                SettingsRow {
                    visible: !Skills.loaded
                    title: I18n.tr("Loading…", "skills list loading state")
                    subtitle: Skills.dir
                }

                // Directories that look like skills but are not (no SKILL.md, or no description).
                // Without this the user just sees "I put it there and nothing appeared".
                Repeater {
                    model: Skills.skipped

                    delegate: SettingsRow {
                        required property var modelData

                        title: modelData.name
                        subtitle: modelData.reason
                    }
                }
            }

            SettingsCard {

                title: I18n.tr("MCP servers", "extensions settings section label")

                Repeater {
                    model: ScriptModel {
                        values: Backend.mcpServers
                    }

                    SettingsRow {
                        id: mcpRow

                        required property var modelData

                        title: modelData.label
                        subtitle: extPage.mcpStatusText(modelData)
                        subtitleColor: modelData.error ? Theme.error : Theme.surfaceVariantText
                        clickable: true
                        showChevron: true
                        onClicked: mcpEditor.open(modelData.id)

                        leading: IconBadge {
                            anchors.verticalCenter: parent.verticalCenter
                            iconName: "extension"
                            containerColor: mcpRow.modelData.error ? Theme.withAlpha(Theme.error, Theme.tonalTintAlpha)
                                            : (mcpRow.modelData.disabled ? Theme.withAlpha(Theme.surfaceText, Theme.tonalTintAlpha)
                                                : Theme.withAlpha(Theme.primary, Theme.tonalTintAlpha))
                            contentColor: mcpRow.modelData.error ? Theme.error
                                            : (mcpRow.modelData.disabled ? Theme.surfaceVariantText : Theme.primary)
                        }

                        // A failed server is never retried automatically (that becomes an infinite
                        // restart loop), so there is an explicit retry button.
                        DActionButton {
                            anchors.verticalCenter: parent.verticalCenter
                            visible: mcpRow.modelData.disabled !== true
                            iconName: "refresh"
                            tooltipText: I18n.tr("Restart", "mcp row restart button tooltip")
                            onClicked: Backend.restartMCPServer(mcpRow.modelData.id)
                        }

                        DActionButton {
                            anchors.verticalCenter: parent.verticalCenter
                            iconName: "delete_outline"
                            iconColor: Theme.error
                            tooltipText: I18n.tr("Remove", "mcp row remove button tooltip")
                            onClicked: {
                                mcpRemove.target = mcpRow.modelData;
                                mcpRemove.show({
                                    "title": I18n.tr("Remove \"%1\"?", "remove mcp server confirmation title").arg(mcpRow.modelData.label),
                                    "message": I18n.tr("Its tools disappear from the agent. The command and environment are not touched.", "remove mcp server confirmation message"),
                                    "confirmText": I18n.tr("Remove", "remove mcp server confirmation button"),
                                    "danger": true
                                });
                            }
                        }
                    }
                }

                SettingsRow {
                    visible: Backend.mcpServers.length === 0
                    title: I18n.tr("No servers configured", "mcp empty state title")
                    subtitle: I18n.tr("Model Context Protocol servers give the agent extra tools.", "mcp empty state description")
                }
            }

            DButton {
                text: I18n.tr("Add MCP server", "add mcp server button")
                iconName: "add"
                onClicked: mcpEditor.open()
            }

            // The window is rebuilt on every open.
            //
            // Keeping a visible=false FloatingWindow around does not work: once closed, setting
            // visible=true does not bring it back (the second press does nothing). Switching tabs
            // only appeared to fix it because the Loader rebuilt the page and with it the window —
            // so that is what this does, deliberately.
            Loader {
                id: mcpEditor
                active: false

                sourceComponent: Component {
                    MCPServerModal {
                        parentWindow: root.hostWindow
                    }
                }

                // An id means editing an existing server; empty means adding. show() resets the
                // form, so rebuilding costs nothing.
                function open(id) {
                    active = false;
                    active = true;
                    if (!item)
                        return;
                    if (id)
                        item.showFor(id);
                    else
                        item.show();
                }
            }

            ConfirmDialog {
                id: mcpRemove

                property var target: null

                onConfirmed: {
                    if (target)
                        Backend.removeMCPServer(target.id);
                    target = null;
                }
            }

            SettingsCard {

                title: I18n.tr("Built-in tools", "extensions settings section label")
                // Real switches: the daemon removes a disabled tool from the list the model sees,
                // rather than rejecting the call — otherwise the model keeps trying.
                SettingsToggleRow {
                    text: I18n.tr("File access", "file access tool toggle label")
                    keywords: I18n.tr("sandbox permissions files read write", "settings search keywords, space separated, add synonyms in your language")
                    description: I18n.tr("Read and write files in the workspace. Off removes the file tools.", "file access tool toggle description")
                    checked: Backend.fileAccess
                    onToggled: checked => Backend.setFileAccess(checked)
                }

                SettingsToggleRow {
                    text: I18n.tr("Shell", "shell tool toggle label")
                    description: I18n.tr("Run shell commands. Off removes the bash tool.", "shell tool toggle description")
                    checked: Backend.shellAccess
                    onToggled: checked => Backend.setShellAccess(checked)
                }

                // The browser has no switch of its own: it shares the composer's web toggle with
                // search and page fetch. This row only says where it is, and why when it is not
                // found.
                SettingsRow {
                    title: I18n.tr("agent-browser", "browser binary row label")
                    subtitle: Backend.browserResolved !== "" ? Backend.browserResolved : Backend.browserReason
                    subtitleColor: Backend.browserResolved === "" ? Theme.warning : Theme.surfaceVariantText

                    DButton {
                        text: I18n.tr("Choose", "browser binary choose button")
                        onClicked: {
                            browserPicker.active = true;
                            const picker = browserPicker.item;
                            picker.browserTitle = I18n.tr("Select agent-browser", "browser binary picker title");
                            picker.open();
                        }
                    }

                    // An empty value falls back to PATH, so it only appears when a path is set.
                    DButton {
                        visible: Backend.browserPath !== ""
                        text: I18n.tr("Use PATH", "browser binary reset to path button")
                        onClicked: Backend.setBrowserPath("")
                    }
                }

                // A fuse, not a quota: hitting it means the model is going in circles, not that
                // the task is too big, so the reply is still finished rather than blocked.
                OptionDropdownRow {
                    text: I18n.tr("Tool steps per reply", "max tool steps dropdown label")
                    keywords: I18n.tr("loop steps limit fuse", "settings search keywords, space separated, add synonyms in your language")
                    description: I18n.tr("Tool rounds per reply. At the limit the last round runs without tools.", "max tool steps dropdown description")
                    optionList: root.toolStepOptions
                    current: Backend.maxToolSteps
                    onPicked: value => Backend.setMaxToolSteps(value)
                }
            }

            Loader {
                id: browserPicker

                active: false
                sourceComponent: FileBrowserModal {
                    parentModal: root.hostWindow
                    bucket: "generic"
                    onAccepted: paths => Backend.setBrowserPath(paths[0] || "")
                }
            }
        }
    }

    // Web search: same shape as the providers tab, far fewer fields. Without a backend the
    // web_search tool is not offered to the model at all (see the agent's tool filter), so the
    // empty state has to state that consequence rather than just "not configured".
    Component {
        id: searchPage

        SettingsPage {
            PageHeader {
                subtitle: I18n.tr("Backends for web_search. web_fetch needs none of them. Keys stay here.", "web search settings section subtitle")
            }

            StyledText {
                visible: Backend.searchProviders.length === 0
                text: I18n.tr("No backend yet. SearXNG needs an instance URL; Brave and Tavily need a key.", "web search empty state")
                font.pixelSize: Theme.fontSizeMedium
                color: Theme.surfaceVariantText
                width: parent.width
                wrapMode: Text.WordWrap
            }

            SettingsGroup {
                Repeater {
                    model: ScriptModel {
                        values: Backend.searchProviders
                    }

                    SettingsRow {
                        id: searchRow

                        required property var modelData

                        title: modelData.label
                        subtitle: {
                            if (modelData.baseUrl)
                                return modelData.baseUrl;
                            if (modelData.hasKey)
                                return I18n.tr("Key set", "search row subtitle when a key is stored");
                            return I18n.tr("API key not set", "search row subtitle when no key is stored");
                        }
                        clickable: true
                        showChevron: true
                        trailingBadge: modelData.active ? I18n.tr("In use", "search row badge for the backend the agent uses") : ""
                        trailingBadgeColor: Theme.primary
                        onClicked: searchAdd.showFor(modelData.id)

                        leading: IconBadge {
                            anchors.verticalCenter: parent.verticalCenter
                            iconName: "search"
                        }

                        DActionButton {
                            anchors.verticalCenter: parent.verticalCenter
                            visible: modelData.active !== true
                            iconName: "check_circle"
                            iconColor: Theme.primary
                            tooltipText: I18n.tr("Use this backend", "search row use button tooltip")
                            onClicked: Backend.useSearchProvider(modelData.id)
                        }

                        DActionButton {
                            anchors.verticalCenter: parent.verticalCenter
                            iconName: "delete_outline"
                            iconColor: Theme.error
                            tooltipText: I18n.tr("Remove", "search row remove button tooltip")
                            onClicked: {
                                searchRemove.target = searchRow.modelData;
                                searchRemove.show({
                                    "title": I18n.tr("Remove \"%1\"?", "remove search backend confirmation title").arg(searchRow.modelData.label),
                                    "message": I18n.tr("The agent loses web search if this was the only backend.", "remove search backend confirmation message"),
                                    "confirmText": I18n.tr("Remove", "remove search backend confirmation button"),
                                    "danger": true
                                });
                            }
                        }
                    }
                }
            }

            // A backend is configured but the composer's toggle is off. That "looks usable, is
            // not" state has to be said here, or the user thinks search is broken.
            SettingsRow {
                visible: Backend.webSearch !== true
                title: I18n.tr("Web search is switched off", "web search page notice when the composer toggle is off")
                subtitle: I18n.tr("Turn it back on with the globe button next to the composer.", "web search page notice when the composer toggle is off, subtitle")
            }

            DButton {
                text: I18n.tr("Add search backend", "add search backend button on web search page")
                iconName: "add"
                onClicked: searchAdd.show()
            }

            ConfirmDialog {
                id: searchRemove

                property var target: null

                onConfirmed: {
                    if (target)
                        Backend.removeSearchProvider(target.id);
                    target = null;
                }
            }
        }
    }

    Component {
        id: aboutPage
        SettingsAboutPage {}
    }
}
