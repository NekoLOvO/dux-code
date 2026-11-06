import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.Widgets
import qs.DCommon.Widgets
import qs.DCommon.FileBrowser

// Sidebar.
//
// Layout follows dux-files' Sidebar.qml: a fixed header (New session + nav) that does not
// scroll, a scrolling area built on DFlickable (scrollbar, momentum, edge fade), and group
// titles reusing FilePlaceSection.
Item {
    id: root

    property string currentView: "chat"

    // Which groups are collapsed (key: group id).
    property var collapsedGroups: ({})

    property string contextSessionId: ""
    property bool contextPinned: false

    function sessionById(id) {
        const list = Sessions.sessions || [];
        for (let i = 0; i < list.length; i++) {
            if (list[i].id === id)
                return list[i];
        }
        return null;
    }

    signal viewChanged(string view)
    signal createSessionRequested
    signal sessionActivated(string sessionId)

    implicitWidth: 256

    function groupLabel(id, fallback) {
        switch (id) {
        case "today":
            return I18n.tr("Today", "session group: today");
        case "week":
            return I18n.tr("This week", "session group: this week");
        case "month":
            return I18n.tr("This month", "session group: this month");
        }
        return fallback;
    }

    function groupExpanded(id) {
        return root.collapsedGroups[id] !== true;
    }

    function toggleGroup(id) {
        const next = Object.assign({}, root.collapsedGroups);
        if (next[id] === true)
            delete next[id];
        else
            next[id] = true;
        root.collapsedGroups = next;
    }

    readonly property var viewItems: [
        {
            "view": "search",
            "label": I18n.tr("Search", "sidebar nav: search"),
            "icon": "search"
        },
        {
            "view": "tasks",
            "label": I18n.tr("Tasks", "sidebar nav: tasks"),
            "icon": "task_alt"
        }
    ]

    // --- shared pieces ---
    component NavRing: Rectangle {
        anchors.fill: parent
        color: "transparent"
        border.width: Theme.focusRingWidth
        border.color: Theme.focusRingColor
        radius: parent.radius
        topLeftRadius: parent.topLeftRadius
        topRightRadius: parent.topRightRadius
        bottomLeftRadius: parent.bottomLeftRadius
        bottomRightRadius: parent.bottomRightRadius
    }

    component GroupRow: StyledRect {
        property bool firstInGroup: false
        property bool lastInGroup: false
        property bool active: false

        width: parent.width
        color: active ? Theme.selectedContainer : "transparent"
        radius: Theme.groupedListInnerRadius
        topLeftRadius: firstInGroup ? Theme.groupedListOuterRadius : radius
        topRightRadius: topLeftRadius
        bottomLeftRadius: lastInGroup ? Theme.groupedListOuterRadius : radius
        bottomRightRadius: bottomLeftRadius
    }

    component PlaceholderRow: GroupRow {
        property alias text: placeholderText.text

        firstInGroup: true
        lastInGroup: true
        height: Theme.buttonHeightS

        StyledText {
            id: placeholderText
            anchors.left: parent.left
            anchors.right: parent.right
            anchors.leftMargin: Theme.spacingM
            anchors.rightMargin: Theme.spacingM
            anchors.verticalCenter: parent.verticalCenter
            font.pixelSize: Theme.fontSizeSmall
            color: Theme.surfaceVariantText
            elide: Text.ElideRight
            horizontalAlignment: Text.AlignLeft
        }
    }

    // Group header (Today / This week / This month); collapsible.
    component GroupHeader: StyledRect {
        id: groupHeader

        property string title: ""
        property bool expanded: true

        signal toggled

        width: parent.width
        height: Theme.buttonHeightXS
        radius: Theme.cornerRadiusS
        color: "transparent"

        Row {
            anchors.left: parent.left
            anchors.leftMargin: Theme.spacingM
            anchors.verticalCenter: parent.verticalCenter
            spacing: Theme.spacingXS

            DIcon {
                name: "expand_more"
                size: Theme.iconSizeSmall
                color: Theme.surfaceVariantText
                rotation: groupHeader.expanded ? 0 : -90
                anchors.verticalCenter: parent.verticalCenter

                Behavior on rotation {
                    enabled: Theme.animationsEnabled

                    NumberAnimation {
                        duration: Theme.expressiveDurations.expressiveEffects
                        easing.type: Easing.BezierSpline
                        easing.bezierCurve: Theme.expressiveCurves.standard
                    }
                }
            }

            StyledText {
                text: groupHeader.title
                font.pixelSize: Theme.fontSizeSmall
                font.weight: Theme.fontWeightMedium
                color: Theme.surfaceVariantText
                anchors.verticalCenter: parent.verticalCenter
            }
        }

        StateLayer {
            stateColor: Theme.surfaceText
            onClicked: groupHeader.toggled()
        }
    }

    // Single row: optional icon + title.
    // Rows use the library's DListItem: grouped radii, selection color, hover layer, ripple,
    // context menu and focus ring come with it.
    component ListRow: DListItem {
        id: listRow

        property string iconName: ""
        property string label: ""
        property bool busy: false
        property bool showSpinner: false
        property string trailingText: ""
        property real indent: 0
        property bool active: false
        property bool pinned: false



        signal activated
        signal contextRequested(real x, real y)

        width: parent?.width ?? 0
        height: Theme.buttonHeightS
        surfaceColor: "transparent"
        isSelected: active
        border.width: 0

        onClicked: listRow.activated()

        onContextMenuRequested: (mouseX, mouseY) => {
            const p = listRow.mapToItem(root, mouseX, mouseY);
            listRow.contextRequested(p.x, p.y);
        }

        // Pinned icon and spinner share one absolutely-positioned slot, so pinning costs no
        // extra indent; the spinner takes over while running.
        DIcon {
            id: pinIcon

            visible: listRow.pinned && !listRow.showSpinner
            name: "push_pin"
            size: Theme.iconSizeSmall
            color: listRow.active ? Theme.onSelectedContainer : Theme.surfaceVariantText
            anchors.left: parent.left
            anchors.leftMargin: Theme.spacingM + Theme.spacingXS
            anchors.verticalCenter: parent.verticalCenter
        }

        // Spinner sits in the indent gap so it does not push the title.
        DSpinner {
            id: spinner

            visible: listRow.showSpinner
            size: Theme.iconSizeSmall
            color: Theme.tertiary
            anchors.left: parent.left
            anchors.leftMargin: Theme.spacingM + Theme.spacingXS
            anchors.verticalCenter: parent.verticalCenter
        }

        StyledText {
            id: trailing

            visible: listRow.trailingText !== ""
            text: listRow.trailingText
            font.pixelSize: Theme.fontSizeSmall
            color: Theme.surfaceVariantText
            anchors.right: parent.right
            anchors.rightMargin: Theme.spacingM
            anchors.verticalCenter: parent.verticalCenter
        }

        Row {
            anchors.left: parent.left
            anchors.right: trailing.left
            anchors.leftMargin: Theme.spacingM + listRow.indent
            anchors.rightMargin: Theme.spacingS
            anchors.verticalCenter: parent.verticalCenter
            spacing: Theme.spacingM

            DIcon {
                visible: listRow.iconName !== ""
                name: listRow.iconName
                size: Theme.iconSizeMedium
                color: listRow.active ? Theme.onSelectedContainer : Theme.surfaceVariantText
                anchors.verticalCenter: parent.verticalCenter
            }

            StyledText {
                text: listRow.label
                width: parent.width - (listRow.iconName !== "" ? Theme.iconSizeMedium + Theme.spacingM : 0)
                font.pixelSize: Theme.fontSizeMedium
                font.weight: listRow.active ? Theme.fontWeightMedium : Theme.fontWeight
                color: listRow.active ? Theme.onSelectedContainer : Theme.surfaceText
                elide: Text.ElideRight
                maximumLineCount: 1
                wrapMode: Text.NoWrap
                anchors.verticalCenter: parent.verticalCenter
            }
        }

        StateLayer {
            stateColor: Theme.surfaceText
            onClicked: listRow.activated()
        }
    }

    property string renameTargetId: ""

    function beginRename(sessionId) {
        const target = root.sessionById(sessionId);
        if (!target)
            return;
        root.renameTargetId = sessionId;
        renameField.text = target.title;
        renameDialog.open();
        Qt.callLater(() => {
            renameField.forceActiveFocus();
            if (renameField.selectAll)
                renameField.selectAll();
        });
    }

    // Shaped like the library FilePicker's "New folder": DDialog + outlined DTextField + two
    // DButtons.
    DankOverlayDialog {
        id: renameDialog

        title: I18n.tr("Rename", "rename session dialog title")
        acceptEnabled: renameField.text.trim() !== ""
        onAccepted: root.commitRename()
        onRejected: root.renameTargetId = ""

        DTextField {
            id: renameField

            width: parent.width
            outlined: true
            labelText: I18n.tr("Name", "rename session dialog field label")
            onAccepted: if (renameDialog.acceptEnabled) root.commitRename()
        }

        actions: [
            DButton {
                text: I18n.tr("Cancel", "rename dialog cancel button")
                variant: "text"
                onClicked: renameDialog.rejected()
            },
            DButton {
                enabled: renameDialog.acceptEnabled
                text: I18n.tr("Rename", "rename dialog confirm button")
                onClicked: renameDialog.accepted()
            }
        ]
    }

    function commitRename() {
        const id = root.renameTargetId;
        const clean = String(renameField.text || "").trim();
        root.renameTargetId = "";
        renameDialog.close();
        if (id === "" || clean === "")
            return;
        Sessions.rename(id, clean);
    }

    function openSessionMenu(sessionId, x, y) {
        root.contextSessionId = sessionId;
        const target = root.sessionById(sessionId);
        root.contextPinned = target ? target.pinned === true : false;
        sessionMenu.openAt(x, y);
    }

    // Names the session with the current model (the daemon only reads the first four messages).
    function smartRename(sessionId) {
        if (sessionId === "")
            return;
        const model = Models.current ? Models.current.id : "";
        Backend.sendRequest("session.smartRename", {
            "sessionId": sessionId,
            "model": model
        }, (res, err) => {
            if (err || !res || !res.title) {
                root.notify(err || I18n.tr("Smart rename failed.", "toast when the model did not return a title"));
                return;
            }
            Sessions.rename(sessionId, res.title);
        });
    }

    // Export as JSON: the raw session structure, readable by other agents.
    function exportSession(sessionId) {
        if (sessionId === "")
            return;
        Backend.sendRequest("session.export", {
            "sessionId": sessionId
        }, (res, err) => {
            if (err || !res || !res.path) {
                root.notify(err || I18n.tr("Export failed.", "toast when exporting a session fails"));
                return;
            }
            root.notify(I18n.tr("Exported to %1", "toast after exporting a session, %1 is the file path").arg(res.path));
        });
    }

    function notify(message) {
        ToastService.info(message);
    }

    // Session context menu (UI only for now; actions come later).
    FileMenu {
        id: sessionMenu

        FileMenuItem {
            text: I18n.tr("Rename", "session context menu action")
            iconName: "edit"
            onClicked: {
                sessionMenu.close();
                root.beginRename(root.contextSessionId);
            }
        }

        FileMenuItem {
            text: I18n.tr("Smart rename", "session context menu action, rename using the model")
            iconName: "auto_awesome"
            onClicked: {
                sessionMenu.close();
                root.smartRename(root.contextSessionId);
            }
        }

        FileMenuItem {
            text: root.contextPinned ? I18n.tr("Unpin", "session context menu action, unpin from the top") : I18n.tr("Pin", "session context menu action, pin to the top")
            iconName: "push_pin"
            onClicked: {
                Sessions.togglePin(root.contextSessionId);
                sessionMenu.close();
            }
        }

        FileMenuItem {
            text: I18n.tr("Export", "session context menu action, export the conversation")
            iconName: "download"
            onClicked: {
                sessionMenu.close();
                root.exportSession(root.contextSessionId);
            }
        }

        FileMenuDivider {}

        FileMenuItem {
            text: I18n.tr("Delete", "session context menu action, delete the conversation")
            iconName: "delete"
            danger: true
            onClicked: {
                Sessions.remove(root.contextSessionId);
                sessionMenu.close();
            }
        }
    }

    // --- fixed header: does not scroll ---
    Column {
        id: stickyHeader

        anchors.top: parent.top
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.margins: Theme.windowInset
        anchors.topMargin: 0
        spacing: Theme.spacingL

        DButton {
            width: parent.width
            text: I18n.tr("New session", "sidebar primary button")
            iconName: "add"
            buttonHeight: Theme.buttonHeightS
            focusPolicy: Qt.NoFocus
            onClicked: root.createSessionRequested()
        }

        Column {
            width: parent.width
            spacing: Theme.groupedListGap

            Repeater {
                id: viewRepeater

                model: ScriptModel {
                    values: root.viewItems
                }

                ListRow {
                    id: viewRow

                    required property int index
                    required property var modelData

                    iconName: modelData.icon
                    label: modelData.label
                    active: root.currentView === modelData.view
                    firstInGroup: index === 0
                    lastInGroup: index === viewRepeater.count - 1
                    onActivated: root.viewChanged(modelData.view)
                }
            }
        }

        // Title is fixed too, not part of the scrolling list.
        FilePlaceSection {
            width: parent.width
            title: I18n.tr("Sessions", "sidebar section title for the session list")
        }
    }

    // --- scrolling area: sessions only ---
    DFlickable {
        id: scroll

        anchors.top: stickyHeader.bottom
        anchors.topMargin: Theme.spacingXS
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.bottom: parent.bottom
        anchors.leftMargin: Theme.windowInset
        anchors.rightMargin: Theme.windowInset
        anchors.bottomMargin: Theme.windowInset

        contentWidth: width
        contentHeight: contentColumn.height
        clip: true

        Column {
            id: contentColumn

            width: scroll.width
            bottomPadding: Theme.spacingL

            Repeater {
                    id: groupRepeater

                    model: ScriptModel {
                        values: Sessions.visibleGroups()
                    }

                    Column {
                        id: sessionGroup

                        required property var modelData

                        width: contentColumn.width
                        spacing: Theme.groupedListGap

                        GroupHeader {
                            visible: sessionGroup.modelData.id !== "pinned"
                            width: parent.width
                            title: root.groupLabel(sessionGroup.modelData.id, sessionGroup.modelData.label)
                            expanded: root.groupExpanded(sessionGroup.modelData.id)
                            onToggled: root.toggleGroup(sessionGroup.modelData.id)
                        }

                        Column {
                            width: parent.width
                            spacing: Theme.groupedListGap
                            visible: sessionGroup.modelData.id === "pinned" || root.groupExpanded(sessionGroup.modelData.id)

                            Repeater {
                                id: sessionRepeater

                                model: ScriptModel {
                                    values: sessionGroup.modelData.sessions
                                }

                                ListRow {
                                    id: sessionRow

                                    required property int index
                                    required property var modelData

                                    label: modelData.title
                                    pinned: modelData.pinned === true
                                    indent: Theme.iconSizeSmall + Theme.spacingXS + Theme.spacingM
                                    busy: modelData.busy === true
                                    showSpinner: modelData.busy === true
                                    // A running reply shows its own age, counted from the daemon's
                                    // start time, so switching sessions does not restart it.
                                    trailingText: modelData.busy === true
                                        ? Sessions.formatElapsed(Sessions.elapsedFor(modelData.id))
                                        : ""
                                    active: Sessions.activeId === modelData.id
                                    firstInGroup: index === 0
                                    lastInGroup: index === sessionRepeater.count - 1
                                    onActivated: {
                                        Sessions.select(modelData.id);
                                        root.sessionActivated(modelData.id);
                                    }
                                    onContextRequested: (x, y) => root.openSessionMenu(modelData.id, x, y)
                                }
                            }
                        }
                    }
                }

            PlaceholderRow {
                // Only say "no sessions" when there really are none; an empty session is not
                // listed but is open right now, so repeating it would confuse.
                visible: Sessions.sessions.length === 0
                text: I18n.tr("No sessions yet", "sidebar placeholder when the session list is empty")
            }
        }
    }
}
