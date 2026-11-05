pragma Singleton
pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import Quickshell.Io
import qs.Common

Singleton {
    id: root

    // Layer outline opacity. Style.layerOutlineWidth is 1 when this is above 0.
    property real blurLayerOutlineOpacity: 0.12
    readonly property string configDir: {
        const xdg = Quickshell.env("XDG_CONFIG_HOME");
        const base = (xdg && xdg !== "") ? xdg : Quickshell.env("HOME") + "/.config";
        return base + "/dcode";
    }
    readonly property string settingsPath: configDir + "/ui-settings.json"

    property alias reasoningByModel: adapter.reasoningByModel
    // "auto" | "light" | "dark"
    property alias themeMode: adapter.themeMode
    // "auto" follows DMS dynamic colors when available, falling back to the
    // chosen preset; "preset" forces a bundled palette; "custom" loads a JSON file;
    // "matugen" loads matugen-theme.json from configDir.
    property alias colorSource: adapter.colorSource
    property alias presetTheme: adapter.presetTheme
    property alias customThemeFile: adapter.customThemeFile
    // 0 (square) … 100; 50 keeps the Material shape scale
    property alias radiusStrength: adapter.radiusStrength
    // 1.0 = opaque. No settings UI yet; Theme.qml reads these directly.
    property alias floatingWindowTransparency: adapter.floatingWindowTransparency
    property alias floatingWindowForegroundLayers: adapter.floatingWindowForegroundLayers
    property alias floatingWindowForegroundTransparency: adapter.floatingWindowForegroundTransparency
    // base transition length in ms, 0 disables animations
    property alias animationDuration: adapter.animationDuration
    property alias reduceMotion: adapter.reduceMotion
    // 0 smooth, 1 balanced, 2 playful
    property alias springBounce: adapter.springBounce
    property alias enableRippleEffects: adapter.enableRippleEffects
    // "comfortable" is the layout everything was designed at; the others scale row padding and
    // list heights through Theme.densityLevel.
    property alias density: adapter.density
    // Ask the compositor to blur what shows through a see-through window.
    property alias blurEnabled: adapter.blurEnabled
    property alias fontSource: adapter.fontSource
    property alias fontFamily: adapter.fontFamily
    property alias monoFontFamily: adapter.monoFontFamily
    property alias displayFontFamily: adapter.displayFontFamily
    property alias fontWeight: adapter.fontWeight
    property alias fontScale: adapter.fontScale
    property alias uiScale: adapter.uiScale
    readonly property real effectiveUiScale: parseFloat(Quickshell.env("QT_SCALE_FACTOR")) || 1
    // QT_SCALE_FACTOR wins; the scale slider is disabled while it is set.
    readonly property bool uiScaleFromEnv: !!Quickshell.env("QT_SCALE_FACTOR") && !Quickshell.env("DUXCODE_UI_SCALE")
    property alias focusRingEnabled: adapter.focusRingEnabled
    property alias focusRingWidth: adapter.focusRingWidth
    // "primary" | "secondary" | "outline" | "surfaceText"
    property alias focusRingColor: adapter.focusRingColor
    property alias windowTitleBars: adapter.windowTitleBars
    // "" follows the system language, otherwise a bundled translation code such as "de" or "zh_CN"
    property alias language: adapter.language
    property alias notificationSounds: adapter.notificationSounds
    // --- Agent ---
    // Working directory handed to the daemon as cwd.
    property alias workingDirectory: adapter.workingDirectory
    // "open" | "closed" | "auto": how thinking blocks and tool cards expand.
    property alias thinkingDisplay: adapter.thinkingDisplay
    property alias toolDisplay: adapter.toolDisplay
    // System notifications, grouped by agent event.
    property alias notifyAgentDone: adapter.notifyAgentDone
    property alias notifyAgentPermission: adapter.notifyAgentPermission
    property alias notifyAgentError: adapter.notifyAgentError
    property alias showTrayIcon: adapter.showTrayIcon
    // "minimize" = hide to tray, "quit" = stop the daemon and child
    property alias closeBehavior: adapter.closeBehavior
    // Sidebar width in pixels (drag the edge).
    property alias sidebarWidth: adapter.sidebarWidth
    // Collapsed outside compact mode.
    property alias sidebarCollapsed: adapter.sidebarCollapsed

    // --- Reasoning level (stored per model) ---
    // Supported levels differ per model, so a global value would send
    // unsupported ones and the request would fail with a 400.
    function reasoningLevel(modelId) {
        const m = reasoningByModel || ({});
        return m[modelId] || "auto";
    }

    function setReasoningLevel(modelId, level) {
        if (!modelId)
            return;
        const next = Object.assign({}, reasoningByModel || ({}));
        if (!level || level === "auto")
            delete next[modelId];
        else
            next[modelId] = level;
        reasoningByModel = next;
    }

    Component.onCompleted: {
        Paths.mkdir(configDir);
    }

    FileView {
        id: settingsFile
        path: root.settingsPath
        // Persistence guards mirror DMS Common/SettingsData.qml: block the
        // initial load so the adapter is populated up front, gate writes on
        // _loaded so the load itself never serializes a half-default adapter
        // back over the saved file, and ignore our own writes via _selfWrite so
        // a write does not trigger a reload storm.
        blockLoading: true
        blockWrites: true
        atomicWrites: true
        watchChanges: true
        printErrors: false

        property bool _loaded: false
        property bool _selfWrite: false

        onLoaded: _loaded = true
        onLoadFailed: _loaded = true
        onFileChanged: {
            if (_selfWrite) {
                _selfWrite = false;
                return;
            }
            reload();
        }
        onAdapterUpdated: {
            if (!_loaded)
                return;
            _selfWrite = true;
            writeAdapter();
        }

        JsonAdapter {
            id: adapter
            property string themeMode: "auto"
            property string colorSource: "auto"
            property string presetTheme: "purple"
            property string customThemeFile: ""
            property int radiusStrength: 50
            property real floatingWindowTransparency: 1.0
            property bool floatingWindowForegroundLayers: true
            property real floatingWindowForegroundTransparency: 1.0
            property int animationDuration: 250
            property bool reduceMotion: false
            property int springBounce: 1
            property bool enableRippleEffects: true
            property string density: "comfortable"
            property bool blurEnabled: false
            property string fontSource: "auto"
            property string fontFamily: ""
            property string monoFontFamily: ""
            property string displayFontFamily: ""
            property int fontWeight: 400
            property real fontScale: 1.0
            property real uiScale: 1.0
            property bool focusRingEnabled: true
            property real focusRingWidth: 1.5
            property string focusRingColor: "primary"
            property bool windowTitleBars: true
            property string language: ""
            property bool notificationSounds: false
            property string workingDirectory: ""
            // "open" always expands, "closed" collapses until clicked,
            // "auto" expands while running.
            property string thinkingDisplay: "auto"
            property string toolDisplay: "auto"
            // { "provider:model": "high" }; a missing key means auto.
            property var reasoningByModel: ({})
            property bool notifyAgentDone: true
            property bool notifyAgentPermission: true
            property bool notifyAgentError: true
            property bool showTrayIcon: true
            property string closeBehavior: "minimize"
            property int sidebarWidth: 272
            property bool sidebarCollapsed: false
        }
    }
}
