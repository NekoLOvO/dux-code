import QtQuick
import Qt.labs.platform as Platform
import qs.Common

Item {
    id: root

    property var shell: null

    function showWindow() {
        if (!root.shell)
            return;
        root.shell.handleWindowAction("show", "");
    }

    function toggleWindow() {
        if (!root.shell)
            return;
        root.shell.handleWindowAction("toggle", "");
    }

    // The SNI item id is read from the application name at registration
    // time, so rename before the icon first becomes visible.
    Component.onCompleted: {
        Qt.application.name = "com.nekolovo.dcode";
        Qt.application.displayName = "Dux Code";
        tray.visible = true;
    }

    Platform.SystemTrayIcon {
        id: tray
        visible: false
        tooltip: "Dux Code"
        icon.source: Qt.resolvedUrl("../assets/com.nekolovo.dcode.svg")

        onActivated: reason => {
            switch (reason) {
            case Platform.SystemTrayIcon.Trigger:
            case Platform.SystemTrayIcon.DoubleClick:
                root.toggleWindow();
                break;
            }
        }

        menu: Platform.Menu {
            Platform.MenuItem {
                text: I18n.tr("Open Dux Code", "tray menu item to show the main window")
                onTriggered: root.showWindow()
            }

            Platform.MenuItem {
                separator: true
            }

            Platform.MenuItem {
                text: I18n.tr("Quit", "tray menu item to quit the application")
                onTriggered: Qt.quit()
            }
        }
    }
}
