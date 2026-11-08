import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets

FloatingWindow {
    id: settingsModal

    property int currentTabIndex: 0
    property bool isCompactMode: width < 640
    property bool menuVisible: false

    // Narrow window: the tab list folds away, and a query keeps only the field on screen so there is
    // somewhere to finish typing while the results take the pane.
    readonly property bool compactSearch: isCompactMode && menuVisible && searching
    readonly property bool searching: settingsContent ? settingsContent.searching : false


    function show() {
        // The daemon can change config.json on its own (a model's output ceiling learned at runtime, MCP
        // servers starting), so every open starts from the file rather than from what was last fetched.
        Backend.refreshConfig();
        visible = true;
    }

    function hide() {
        visible = false;
    }

    // For the command line: qs ipc call dcode provider <id>.
    function openProviderSetup(providerId) {
        show();
        if (settingsContent)
            settingsContent.openProviderSetup(providerId);
    }

    onClosed: hide()

    title: I18n.tr("Settings", "settings window title")
    minimumSize: Qt.size(520, 420)
    implicitWidth: Math.max(minimumSize.width, Theme.modalWidth(parentWindow, screen, 880))
    implicitHeight: Math.max(minimumSize.height, Theme.modalHeight(parentWindow, screen, 720))
    // Alpha-aware: this is what the opacity setting changes.
    color: Theme.windowSurface
    surfaceFormat.opaque: false

    // Asks the compositor to blur what shows through, when the window is see-through and the user
    // asked for it. On a compositor without the protocol this is a silent no-op rather than an error,
    // which is why the setting says so instead of being gated on a probe.
    WindowBlur {
        targetWindow: settingsModal
        blurEnabled: Theme.translucentWindows
        blurWidth: settingsModal.width
        blurHeight: settingsModal.height
    }
    visible: false

    onIsCompactModeChanged: menuVisible = false

    FocusScope {
        anchors.fill: parent
        focus: true

        LayoutMirroring.enabled: I18n.isRtl
        LayoutMirroring.childrenInherit: true

        Keys.onPressed: event => {
            if (event.key === Qt.Key_Escape) {
                if (!sidebar.clearHighlight())
                    settingsModal.hide();
                event.accepted = true;
                return;
            }
            if (contentScope.activeFocus || !sidebar.visible)
                return;
            switch (event.key) {
            case Qt.Key_Down:
            case Qt.Key_J:
                sidebar.moveHighlight(1);
                break;
            case Qt.Key_Up:
            case Qt.Key_K:
                sidebar.moveHighlight(-1);
                break;
            case Qt.Key_Return:
            case Qt.Key_Enter:
                if (!sidebar.selectHighlighted())
                    return;
                break;
            default:
                return;
            }
            event.accepted = true;
        }

        Column {
            anchors.fill: parent
            spacing: 0

            DWindowHeader {
                id: header
                width: parent.width
                z: 10
                controls: windowControls
                title: I18n.tr("Settings", "settings window header title")
                onCloseRequested: settingsModal.hide()

                DActionButton {
                    visible: settingsModal.isCompactMode
                    iconName: "menu"
                    buttonSize: Theme.buttonHeightXXS
                    Accessible.name: I18n.tr("Menu", "settings header button that shows the section list on narrow windows")
                    onClicked: settingsModal.menuVisible = !settingsModal.menuVisible
                }
            }

            Item {
                width: parent.width
                height: parent.height - header.height
                clip: true

                SettingsSidebar {
                    id: sidebar
                    anchors.left: parent.left
                    anchors.top: parent.top
                    anchors.bottom: settingsModal.compactSearch ? undefined : parent.bottom
                    height: settingsModal.compactSearch ? implicitHeight : undefined
                    width: settingsModal.isCompactMode ? parent.width : implicitWidth
                    visible: settingsModal.isCompactMode ? settingsModal.menuVisible : true
                    fieldOnly: settingsModal.compactSearch
                    currentIndex: settingsModal.currentTabIndex
                    matchCounts: settingsContent ? settingsContent.searchCounts : []
                    onTabSelected: index => {
                        settingsModal.currentTabIndex = index;
                        sidebar.clearSearch();
                        if (settingsModal.isCompactMode)
                            settingsModal.menuVisible = false;
                    }
                    onSearchAccepted: settingsContent.pickFirst()
                }

                FocusScope {
                    id: contentScope

                    readonly property real paneInset: SettingsMetrics.paneMargin

                    anchors.left: settingsModal.isCompactMode ? (settingsModal.menuVisible ? sidebar.right : parent.left) : sidebar.right
                    anchors.right: parent.right
                    anchors.top: parent.top
                    anchors.bottom: parent.bottom
                    anchors.leftMargin: settingsModal.isCompactMode ? paneInset : 0
                    anchors.rightMargin: paneInset
                    anchors.bottomMargin: paneInset

                    Rectangle {
                        anchors.fill: parent
                        radius: SettingsMetrics.paneRadius
                        color: SettingsMetrics.paneColor
                    }

                    SettingsContent {
                        id: settingsContent

                        // One source of truth: the sidebar owns the field, the pane renders what it says.
                        searchQuery: sidebar.query
                        onSearchDismissed: sidebar.clearSearch()
                        onPageRequested: page => settingsModal.currentTabIndex = page
                        anchors.fill: parent
                        anchors.leftMargin: SettingsMetrics.panePadding
                        anchors.rightMargin: SettingsMetrics.panePadding
                        currentIndex: settingsModal.currentTabIndex
                        hostWindow: settingsModal
                    }
                }
            }
        }
    }

    FloatingWindowControls {
        id: windowControls
        targetWindow: settingsModal
    }

    Toast {
        anchors.fill: parent
        z: 102
    }
}
