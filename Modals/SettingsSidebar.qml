import QtQuick
import qs.Common
import qs.Widgets
import qs.DCommon.Widgets

Item {
    id: root

    property int currentIndex: 0
    property int highlightIndex: -1
    // Hits per page, for the count beside each tab. -1 means "not searching".
    property var matchCounts: []
    // In compact mode the tab list folds away and only the field stays, so the search is reachable
    // without opening the menu first.
    property bool fieldOnly: false
    property alias query: searchField.text

    signal tabSelected(int index)
    // Enter in the field: open the best match instead of making the user click it.
    signal searchAccepted

    function clearSearch() {
        if (searchField.text === "")
            return false;
        searchField.text = "";
        return true;
    }

    function tabOrder() {
        return groups.reduce((all, group) => all.concat(group.map(tab => tab.index)), []);
    }

    function moveHighlight(delta) {
        const order = tabOrder();
        let position = order.indexOf(highlightIndex);
        if (position < 0)
            position = order.indexOf(currentIndex);
        highlightIndex = order[(position + delta + order.length) % order.length];
    }

    function selectHighlighted() {
        if (highlightIndex < 0)
            return false;
        tabSelected(highlightIndex);
        highlightIndex = -1;
        return true;
    }

    function clearHighlight() {
        if (highlightIndex < 0)
            return false;
        highlightIndex = -1;
        return true;
    }

    readonly property var groups: [[
            {
                accent: "blue",
                index: 0,
                label: I18n.tr("General", "settings sidebar tab label"),
                hint: I18n.tr("Application and agent defaults", "settings sidebar tab hint"),
                icon: "tune"
            },
            {
                accent: "blue",
                index: 1,
                label: I18n.tr("Appearance", "settings sidebar tab label"),
                hint: I18n.tr("Theme, shape and motion", "settings sidebar tab hint"),
                icon: "palette"
            }
        ], [
            {
                accent: "green",
                index: 2,
                label: I18n.tr("Providers", "settings sidebar tab label"),
                hint: I18n.tr("Models and API keys", "settings sidebar tab hint"),
                icon: "cloud"
            },
            {
                accent: "green",
                index: 3,
                label: I18n.tr("Extensions", "settings sidebar tab label"),
                hint: I18n.tr("Skills, MCP and tools", "settings sidebar tab hint"),
                icon: "extension"
            },
            {
                accent: "green",
                index: 4,
                label: I18n.tr("Web search", "settings sidebar tab label"),
                hint: I18n.tr("Search backends", "settings sidebar tab hint"),
                icon: "search"
            },
            {
                accent: "green",
                index: 5,
                label: I18n.tr("Notifications", "settings sidebar tab label"),
                hint: I18n.tr("Alerts and sounds", "settings sidebar tab hint"),
                icon: "notifications"
            }
        ], [
            {
                accent: "purple",
                index: 6,
                label: I18n.tr("About", "settings sidebar tab label"),
                hint: I18n.tr("Version and links", "settings sidebar tab hint"),
                icon: "info"
            }
        ]]

    implicitWidth: SettingsMetrics.sidebarWidth
    implicitHeight: fieldOnly ? searchField.height + navColumn.padding * 2 : 0

    DFlickable {
        anchors.fill: parent
        clip: true
        contentWidth: width
        contentHeight: navColumn.height

        Column {
            id: navColumn
            width: parent.width
            padding: SettingsMetrics.paneMargin
            topPadding: 0
            spacing: SettingsMetrics.sidebarGroupGap

            DSearchField {
                id: searchField

                width: navColumn.width - navColumn.padding * 2
                backgroundColor: Theme.surfaceContainerLowest
                placeholderText: I18n.tr("Search settings", "placeholder in the settings search field")
                onAccepted: root.searchAccepted()
            }

            Repeater {
                model: root.fieldOnly ? [] : root.groups

                Column {
                    id: groupColumn
                    required property var modelData
                    width: navColumn.width - navColumn.padding * 2
                    spacing: Theme.groupedListGap

                    Repeater {
                        model: groupColumn.modelData

                        SettingsSidebarItem {
                            required property var modelData
                            required property int index
                            width: groupColumn.width
                            iconName: modelData.icon
                            title: modelData.label
                            hint: modelData.hint
                            accent: modelData.accent
                            active: root.currentIndex === modelData.index
                            highlighted: root.highlightIndex === modelData.index
                            matchCount: root.matchCounts[modelData.index] ?? -1
                            firstInGroup: index === 0
                            lastInGroup: index === groupColumn.modelData.length - 1
                            onActiveFocusChanged: {
                                if (activeFocus)
                                    root.highlightIndex = modelData.index;
                            }
                            Keys.onReturnPressed: event => event.accepted = root.selectHighlighted()
                            Keys.onEnterPressed: event => event.accepted = root.selectHighlighted()
                            Keys.onSpacePressed: event => event.accepted = root.selectHighlighted()
                            onClicked: {
                                root.highlightIndex = -1;
                                root.tabSelected(modelData.index);
                            }
                        }
                    }
                }
            }
        }
    }
}
