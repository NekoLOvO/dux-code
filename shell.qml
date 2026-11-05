//@ pragma Env QT_WAYLAND_DISABLE_WINDOWDECORATION=1
//@ pragma Env QSG_RENDER_LOOP=threaded
//@ pragma Env QT_QUICK_CONTROLS_STYLE=Material
//@ pragma UseQApplication
//@ pragma AppId com.nekolovo.dcode

import QtQuick
import Quickshell
import Quickshell.Io
import Quickshell.Wayland
import qs.Common
import qs.DCommon.Common as DC
import qs.Modules
import qs.Services

ShellRoot {
    id: root

    readonly property var log: Log.scoped("Shell")
    readonly property bool enableHotReload: Quickshell.env("DUXCODE_ENABLE_HOTRELOAD") === "1" || Quickshell.env("DUXCODE_ENABLE_HOTRELOAD") === "true"

    Component.onCompleted: {
        Quickshell.watchFiles = enableHotReload;
        DC.Style.theme = Theme;
        DC.Style.settings = SettingsData;
        DC.I18n.backend = I18n;
        DC.Paths.backend = Paths;
        DC.Log.backend = Log;
        DC.Host.cache = CacheData;
        DC.Host.files = FilesService;
        DC.Host.ownWindowDecorations = Qt.binding(() => SettingsData.windowTitleBars);
    }

    function ownToplevel() {
        const toplevels = ToplevelManager.toplevels;
        if (!toplevels || !toplevels.values)
            return null;

        for (const t of toplevels.values) {
            if (t.appId === Quickshell.appId)
                return t;
        }
        return null;
    }

    function focusToplevel() {
        const t = ownToplevel();
        if (!t)
            return;
        if (t.minimized)
            t.minimized = false;
        t.activate();
    }

    function showAndFocus() {
        if (windowLoader.active) {
            focusToplevel();
            return;
        }
        windowLoader.active = true;
        focusRetry.restart();
    }

    function handleWindowAction(action) {
        switch (action) {
        case "show":
            showAndFocus();
            break;
        case "hide":
            windowLoader.active = false;
            break;
        case "toggle":
            {
                const t = windowLoader.active ? ownToplevel() : null;
                if (t && !t.minimized) {
                    windowLoader.active = false;
                    return;
                }
                showAndFocus();
                break;
            }
        }
    }

    function handleNewSession() {
        showAndFocus();
        Qt.callLater(() => {
            if (windowLoader.item)
                windowLoader.item.newSession();
        });
    }

    Timer {
        id: focusRetry
        interval: 150
        repeat: false
        onTriggered: root.focusToplevel()
    }

    Loader {
        id: trayLoader
        active: SettingsData.showTrayIcon && Quickshell.env("DUXCODE_NO_TRAY") !== "1"
        source: Qt.resolvedUrl("Modules/TrayIcon.qml")
        onLoaded: item.shell = root
        onStatusChanged: {
            if (status === Loader.Error)
                root.log.warn("tray icon unavailable (Qt.labs.platform missing?)");
        }
    }

    LazyLoader {
        id: windowLoader
        active: Quickshell.env("DUXCODE_START_HIDDEN") !== "1"

        ChatWindow {
            onHideRequested: windowLoader.active = false
        }
    }

    // Whether the main window has focus. FloatingWindow exposes no "active";
    // Toplevel.activated is reliable and reads false while the Loader is unloaded.
    readonly property bool windowFocused: {
        const list = ToplevelManager.toplevels ? ToplevelManager.toplevels.values : null;
        if (!list)
            return false;
        for (let i = 0; i < list.length; i++) {
            if (list[i].appId !== Quickshell.appId)
                continue;
            return list[i].activated === true;
        }
        return false;
    }

    // Only notify when the window is not in front. Also instantiates the
    // Notifier singleton while the Loader is closed, so agent events still land.
    Binding {
        target: Notifier
        property: "windowActive"
        value: root.windowFocused
        restoreMode: Binding.RestoreNone
    }

    // Public command interface: qs ipc call dcode <function> [args].
    IpcHandler {
        target: "dcode"

        function ping(): string {
            return "pong";
        }

        // Not `show`: `qs ipc show` is a subcommand that prints the target list, so a trailing
        // `show` positional gets eaten by the CLI and the call silently does nothing (exit 0).
        function raise(): void {
            root.showAndFocus();
        }

        function hide(): void {
            root.handleWindowAction("hide");
        }

        function toggle(): void {
            root.handleWindowAction("toggle");
        }

        function newSession(): void {
            root.handleNewSession();
        }

        function settings(tab: int): void {
            root.showAndFocus();
            const win = windowLoader.item;
            if (win && win.openSettings)
                win.openSettings(tab);
        }

        // Empty string opens "add provider"; an id opens that provider's settings.
        // No showAndFocus: the settings window raises itself, and focusing the
        // main window would cover what the user is looking at.
        function provider(id: string): void {
            const win = windowLoader.item;
            if (win && win.openProviderSetup)
                win.openProviderSetup(id);
        }

        function send(text: string): void {
            root.showAndFocus();
            Qt.callLater(() => {
                Chat.send(text);
            });
        }

        function status(): string {
            return JSON.stringify({
                "connected": Backend.connected,
                "session": Sessions.activeId,
                "busy": Chat.busy,
                "model": Models.currentId,
                "portalAvailable": PortalService.available,
                "colorScheme": PortalService.systemColorScheme,
                "isLight": Theme.isLightMode
            });
        }
    }
}
