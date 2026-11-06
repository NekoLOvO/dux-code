import QtQuick
import Quickshell
import Quickshell.Wayland
import qs.Common
import qs.Modals
import qs.Services
import qs.Widgets
import qs.DCommon.Common as DC
import qs.DCommon.Widgets

// Main window. Same skeleton as CalendarWindow: FloatingWindow + DWindowHeader + resizable
// sidebar + rounded content panel. Sidebar animation mirrors dux-files' WindowSidebarHost:
// docked animates the slot width, compact animates the overlay x.
FloatingWindow {
    id: window

    signal hideRequested

    property bool isCompactMode: width < 760
    property bool menuVisible: false

    // 0 = collapsed, 1 = expanded. Every offset derives from this value, so a single
    // Behavior on it animates the whole collapse.
    property real sidebarReveal: 1

    function syncReveal() {
        const wantOpen = isCompactMode ? menuVisible : !SettingsData.sidebarCollapsed;
        sidebarReveal = wantOpen ? 1 : 0;
    }

    function toggleSidebar() {
        if (isCompactMode)
            menuVisible = !menuVisible;
        else
            SettingsData.sidebarCollapsed = !SettingsData.sidebarCollapsed;
    }

    function openSettings(tabIndex) {
        settingsLoader.active = true;
        if (settingsLoader.item) {
            if (tabIndex !== undefined && tabIndex >= 0)
                settingsLoader.item.currentTabIndex = tabIndex;
            settingsLoader.item.show();
        }
    }

    function openProviderSetup(providerId) {
        settingsLoader.active = true;
        if (settingsLoader.item) {
            settingsLoader.item.currentTabIndex = 2;
            if (settingsLoader.item.openProviderSetup)
                settingsLoader.item.openProviderSetup(providerId);
            else
                settingsLoader.item.show();
        }
    }

    function newSession() {
        Sessions.create();
        if (isCompactMode)
            menuVisible = false;
    }

    implicitWidth: 1100
    implicitHeight: screen ? Math.min(820, screen.height - 100) : 820
    minimumSize: Qt.size(560, 480)
    // Alpha-aware: this is what the opacity setting changes.
    color: Theme.windowSurface
    surfaceFormat.opaque: false

    // Asks the compositor to blur what shows through, when the window is see-through and the user
    // asked for it. On a compositor without the protocol this is a silent no-op rather than an error,
    // which is why the setting says so instead of being gated on a probe.
    WindowBlur {
        targetWindow: window
        blurEnabled: Theme.translucentWindows
        blurWidth: window.width
        blurHeight: window.height
    }
    title: Chat.busy ? "Code — " + I18n.tr("generating", "window title suffix while a reply streams") : "Code"
    onClosed: hideRequested()

    Component.onCompleted: {
        syncReveal();
        // The dialogs below are part of the window, not of the daemon's state: the window can be opened
        // while a permission request or a question is already waiting for an answer.
        if (Backend.permissionRequests.length > 0)
            permissionLoader.active = true;
        if (Backend.questions.length > 0)
            questionLoader.active = true;
    }

    Behavior on sidebarReveal {
        enabled: Theme.animationsEnabled

        NumberAnimation {
            duration: Theme.expressiveDurations.expressiveFastSpatial
            easing.type: Easing.BezierSpline
            easing.bezierCurve: Theme.expressiveCurves.standard
        }
    }

    onIsCompactModeChanged: syncReveal()

    Connections {
        target: SettingsData
        function onSidebarCollapsedChanged() {
            window.syncReveal();
        }
    }

    onMenuVisibleChanged: syncReveal()

    Shortcut {
        sequence: "Ctrl+N"
        onActivated: window.newSession()
    }

    Shortcut {
        sequence: "Ctrl+B"
        onActivated: window.toggleSidebar()
    }

    Shortcut {
        sequence: "Esc"
        onActivated: {
            if (window.isCompactMode && window.menuVisible)
                window.menuVisible = false;
        }
    }

    Column {
        anchors.fill: parent
        spacing: 0

        DWindowHeader {
            id: header
            width: parent.width
            z: 10
            controls: windowControls
            title: window.title
            onCloseRequested: window.hideRequested()
        }

        Item {
            id: bodyArea
            width: parent.width
            height: parent.height - header.height
            clip: true

            readonly property real minSidebarWidth: 220
            readonly property real maxSidebarWidth: Math.max(minSidebarWidth, width - 360)
            readonly property real expandedWidth: Math.max(minSidebarWidth, Math.min(maxSidebarWidth, sidebarSlot.preferredWidth))

            Connections {
                target: SettingsData
                function onSidebarWidthChanged() {
                    if (!sidebarResizeArea.pressed)
                        sidebarSlot.preferredWidth = SettingsData.sidebarWidth;
                }
            }

            // --- Docked: slot width animation + clip ---
            Item {
                id: sidebarSlot

                property real preferredWidth: SettingsData.sidebarWidth

                anchors.top: parent.top
                anchors.bottom: parent.bottom
                anchors.left: parent.left
                width: window.isCompactMode ? 0 : window.sidebarReveal * bodyArea.expandedWidth
                clip: true
                visible: width > 0

                ChatSidebar {
                    id: sidebar
                    anchors.left: parent.left
                    anchors.top: parent.top
                    anchors.bottom: parent.bottom
                    width: bodyArea.expandedWidth
                    currentView: "chat"
                    onViewChanged: view => {
                        if (window.isCompactMode)
                            window.menuVisible = false;
                        if (view === "search")
                            window.openSearch();
                        else if (view === "tasks")
                            window.openTasks();
                    }
                    onCreateSessionRequested: window.newSession()
                    onSessionActivated: id => {
                        if (window.isCompactMode)
                            window.menuVisible = false;
                    }
                }
            }

            // --- resize handle ---
            Item {
                id: sidebarResizer

                anchors.left: sidebarSlot.right
                anchors.leftMargin: -width / 2
                anchors.top: parent.top
                anchors.bottom: parent.bottom
                width: Theme.spacingS
                z: 10
                visible: !window.isCompactMode && window.sidebarReveal > 0.98

                Rectangle {
                    anchors.horizontalCenter: parent.horizontalCenter
                    width: Theme.outlineWidthFocused
                    height: parent.height
                    radius: Theme.fullRadius(width, height)
                    color: Theme.primary
                    visible: sidebarResizeArea.containsMouse || sidebarResizeArea.pressed
                }

                MouseArea {
                    id: sidebarResizeArea

                    property real pressX: 0
                    property real startWidth: 0

                    anchors.fill: parent
                    anchors.leftMargin: -Theme.spacingXS
                    anchors.rightMargin: -Theme.spacingXS
                    hoverEnabled: true
                    cursorShape: Qt.SizeHorCursor
                    acceptedButtons: Qt.LeftButton
                    onPressed: mouse => {
                        pressX = mapToItem(bodyArea, mouse.x, 0).x;
                        startWidth = sidebarSlot.width;
                    }
                    onPositionChanged: mouse => {
                        if (!pressed)
                            return;
                        const delta = mapToItem(bodyArea, mouse.x, 0).x - pressX;
                        const wanted = startWidth + (I18n.isRtl ? -delta : delta);
                        sidebarSlot.preferredWidth = Math.max(bodyArea.minSidebarWidth, Math.min(bodyArea.maxSidebarWidth, wanted));
                    }
                    onReleased: SettingsData.sidebarWidth = Math.round(sidebarSlot.preferredWidth)
                    onCanceled: sidebarSlot.preferredWidth = SettingsData.sidebarWidth
                    onDoubleClicked: {
                        sidebarSlot.preferredWidth = 240;
                        SettingsData.sidebarWidth = 240;
                    }
                }
            }

            // --- content panel ---
            Rectangle {
                id: pane

                anchors.right: parent.right
                anchors.top: parent.top
                anchors.bottom: parent.bottom
                anchors.left: window.isCompactMode ? parent.left : sidebarSlot.right
                anchors.leftMargin: window.isCompactMode ? Theme.windowInset : Theme.windowInset * (1 - window.sidebarReveal)
                anchors.rightMargin: Theme.windowInset
                anchors.bottomMargin: Theme.windowInset
                radius: Theme.cornerRadiusL
                color: Theme.floatingWindowPaneSurface
                clip: true

                ChatContent {
                    anchors.fill: parent
                    menuButtonVisible: true
                    hostWindow: window
                    onMenuRequested: window.toggleSidebar()
                    onSettingsRequested: window.openSettings()
                }
            }

            // --- Compact: overlay sliding in from the left ---
            Rectangle {
                id: scrim

                anchors.fill: parent
                visible: opacity > 0
                opacity: window.isCompactMode ? window.sidebarReveal : 0
                color: Theme.withAlpha(Theme.scrimColor, Theme.scrimAlpha)
                z: 20

                MouseArea {
                    anchors.fill: parent
                    onClicked: window.menuVisible = false
                }
            }

            Rectangle {
                id: sidebarOverlay

                readonly property real overlayWidth: Math.max(bodyArea.minSidebarWidth, Math.min(parent.width - Theme.windowInset * 2, sidebarSlot.preferredWidth))

                anchors.top: parent.top
                anchors.bottom: parent.bottom
                width: overlayWidth
                x: window.isCompactMode ? -width + window.sidebarReveal * width : -width
                visible: x > -width
                z: 21
                color: Theme.surfaceContainerLow
                topRightRadius: Theme.cornerRadiusL
                bottomRightRadius: Theme.cornerRadiusL

                DC.ElevationShadow {
                    anchors.fill: parent
                    z: -1
                    level: Theme.elevationLevel1
                    targetRadius: Theme.cornerRadiusL
                    targetColor: sidebarOverlay.color
                    shadowEnabled: Theme.elevationEnabled
                }

                ChatSidebar {
                    anchors.fill: parent
                    currentView: "chat"
                    onViewChanged: view => {
                        window.menuVisible = false;
                        if (view === "search")
                            window.openSearch();
                        else if (view === "tasks")
                            window.openTasks();
                    }
                    onCreateSessionRequested: window.newSession()
                    onSessionActivated: id => {
                        window.menuVisible = false;
                    }
                }
            }
        }
    }

    FloatingWindowControls {
        id: windowControls
        targetWindow: window
    }

    Loader {
        id: settingsLoader
        active: false
        sourceComponent: Component {
            SettingsModal {
                parentWindow: window
            }
        }
    }

    // --- the four dialogs, built when first needed ---
    //
    // Search and tasks are opened from the sidebar, so they used to be built with the window even for
    // someone who never opens them (308 + 515 lines of QML, all of it load time). Same Loader shape as
    // settings above: the object appears on the first open.
    //
    // Permission and questions are not click-driven — the daemon decides when they appear — so they
    // cannot wait for a click. Both open themselves in Component.onCompleted when a request is already
    // waiting (Backend appends to the list before emitting, so the order holds), which means a
    // Connections here is enough: the same event activates the loader, and the new object finds the
    // request on its own.
    Loader {
        id: searchLoader
        active: false
        sourceComponent: Component {
            SearchModal {
                parentWindow: window
            }
        }
    }

    Loader {
        id: taskLoader
        active: false
        sourceComponent: Component {
            TaskModal {
                parentWindow: window
            }
        }
    }

    // A Loader is synchronous, so item exists on the next line — guarded anyway, because a component
    // that failed to load leaves item null and a TypeError in a click handler hides the real error.
    function openSearch() {
        searchLoader.active = true;
        if (searchLoader.item)
            searchLoader.item.show();
    }

    function openTasks() {
        taskLoader.active = true;
        if (taskLoader.item)
            taskLoader.item.show();
    }

    Toast {
        anchors.fill: parent
    }

    Loader {
        id: permissionLoader
        active: false
        sourceComponent: Component {
            PermissionDialog {}
        }
    }

    Loader {
        id: questionLoader
        active: false
        sourceComponent: Component {
            QuestionDialog {}
        }
    }

    Connections {
        target: Backend

        function onPermissionPending() {
            permissionLoader.active = true;
        }

        function onQuestionPending() {
            questionLoader.active = true;
        }
    }

    Connections {
        target: Chat

        // The ask_user tool card, for a question that was put aside.
        function onQuestionRestoreRequested() {
            questionLoader.active = true;
        }
    }
}
