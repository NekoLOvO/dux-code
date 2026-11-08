import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.DCommon.Widgets

// Cross-session search.
//
// Searches message bodies (tool output included). Every hit carries a messageId rather than an
// index: loading a session merges consecutive tool results into one row and drops empty
// assistant messages, so the store's Nth entry is not the screen's Nth row.
//
// After a click (or Enter): switch session → wait for Chat to finish loading → MessageList
// scrolls to that message and highlights it.
FloatingWindow {
    id: modal

    property var hits: []
    property string query: ""
    property int selectedIndex: -1
    property bool searching: false
    property string errorText: ""

    function show() {
        query = "";
        hits = [];
        selectedIndex = -1;
        searching = false;
        errorText = "";
        visible = true;
        searchField.forceActiveFocus();
    }

    function hide() {
        visible = false;
    }

    function runSearch() {
        const q = String(query).trim();
        if (q === "") {
            hits = [];
            selectedIndex = -1;
            searching = false;
            return;
        }
        searching = true;
        errorText = "";
        Backend.sendRequest("session.search", {
            "query": q,
            "limit": 50
        }, (res, err) => {
            // The query may have changed while this was in flight; drop stale results.
            if (String(modal.query).trim() !== q)
                return;
            searching = false;
            if (err || !res) {
                errorText = err || I18n.tr("Search failed", "search modal error");
                hits = [];
                selectedIndex = -1;
                return;
            }
            modal.hits = res.hits || [];
            modal.selectedIndex = modal.hits.length > 0 ? 0 : -1;
        });
    }

    function moveSelection(delta) {
        if (hits.length === 0)
            return;
        selectedIndex = Math.max(0, Math.min(hits.length - 1, selectedIndex + delta));
        resultList.positionViewAtIndex(selectedIndex, ListView.Contain);
    }

    function openHit(index) {
        if (index < 0 || index >= hits.length)
            return;
        const h = hits[index];
        hide();
        Chat.jumpTo(h.sessionId, h.messageId);
    }

    function roleLabel(role) {
        if (role === "user")
            return I18n.tr("You", "search result: the message is from the user");
        if (role === "tool")
            return I18n.tr("Tool", "search result: the hit is in a tool result");
        return I18n.tr("Assistant", "search result: the message is from the model");
    }

    function whenLabel(millis) {
        if (!millis)
            return "";
        const d = new Date(millis);
        const now = new Date();
        if (d.toDateString() === now.toDateString())
            return Qt.formatDateTime(d, "HH:mm");
        return Qt.formatDateTime(d, "MM-dd HH:mm");
    }

    title: I18n.tr("Search", "search modal window title")
    minimumSize: Qt.size(520, 420)
    implicitWidth: Math.max(minimumSize.width, Theme.modalWidth(parentWindow, screen, 680))
    implicitHeight: Math.max(minimumSize.height, Math.min(screen ? screen.height - 80 : 900, 580))
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

    FloatingWindowControls {
        id: windowControls
        targetWindow: modal
    }

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
        }

        Item {
            width: parent.width
            height: parent.height - headerRow.height
            clip: true

            Column {
                anchors.fill: parent
                anchors.margins: Theme.spacingL
                spacing: Theme.spacingM

                DTextField {
                    id: searchField

                    width: parent.width
                    outlined: true
                    placeholderText: I18n.tr("Search all conversations", "search modal field placeholder")
                    leftIconName: "search"
                    text: modal.query
                    onTextChanged: {
                        modal.query = text;
                        debounce.restart();
                    }

                    // DTextField has no accepted signal, so take the key here: focus lives in its
                    // inner TextInput and unhandled keys bubble up.
                    Keys.onPressed: event => {
                        if (event.key === Qt.Key_Down) {
                            modal.moveSelection(1);
                            event.accepted = true;
                        } else if (event.key === Qt.Key_Up) {
                            modal.moveSelection(-1);
                            event.accepted = true;
                        } else if (event.key === Qt.Key_Return || event.key === Qt.Key_Enter) {
                            modal.openHit(modal.selectedIndex >= 0 ? modal.selectedIndex : 0);
                            event.accepted = true;
                        } else if (event.key === Qt.Key_Escape) {
                            modal.hide();
                            event.accepted = true;
                        }
                    }
                }

                // Search as you type, but do not hit the daemon on every keystroke.
                Timer {
                    id: debounce

                    interval: 220
                    onTriggered: modal.runSearch()
                }

                // Status line: searching / hit count / no query.
                StyledText {
                    width: parent.width
                    font.pixelSize: Theme.fontSizeSmall
                    color: modal.errorText !== "" ? Theme.error : Theme.surfaceVariantText
                    wrapMode: Text.WordWrap
                    text: {
                        if (modal.errorText !== "")
                            return modal.errorText;
                        if (String(modal.query).trim() === "")
                            return I18n.tr("Type to search every conversation.", "search modal hint before typing");
                        if (modal.searching)
                            return I18n.tr("Searching…", "search modal while a query is in flight");
                        if (modal.hits.length === 0)
                            return I18n.tr("No matches", "search modal when nothing was found");
                        return I18n.tr("%1 matches", "search modal result count").arg(modal.hits.length);
                    }
                }

                // DListView from DCommon: scrollbar, wheel, trackpad and edge fade come with it
                // and behave like the settings page and the file browser.
                DListView {
                    id: resultList

                    width: parent.width
                    height: parent.height - y
                    clip: true
                    spacing: Theme.spacingXS
                    model: modal.hits
                    boundsBehavior: Flickable.StopAtBounds
                    showScrollBar: true

                    delegate: Rectangle {
                        id: hitRow

                        required property int index
                        required property var modelData

                        readonly property bool current: index === modal.selectedIndex

                        width: ListView.view ? ListView.view.width : 0
                        height: hitColumn.implicitHeight + Theme.spacingM * 2
                        radius: Theme.cornerRadiusM
                        // Has a background even at rest (the list is a set of pills). The hover
                        // step comes from the StateLayer below rather than from
                        // surfaceContainerHighest, which falls back to high when the theme has no
                        // surface_container_highest (Theme.qml:209) — same colour, so hover would
                        // not show.
                        color: current ? Theme.selectedContainer : Theme.surfaceContainerHigh

                        Column {
                            id: hitColumn

                            anchors.left: parent.left
                            anchors.right: parent.right
                            anchors.verticalCenter: parent.verticalCenter
                            anchors.leftMargin: Theme.spacingM
                            anchors.rightMargin: Theme.spacingM
                            spacing: Theme.spacingXS

                            StyledText {
                                width: parent.width
                                text: hitRow.modelData.sessionTitle || I18n.tr("Untitled", "search result: session without a title")
                                font.pixelSize: Theme.fontSizeSmall
                                font.weight: Font.DemiBold
                                color: Theme.surfaceText
                                elide: Text.ElideRight
                                maximumLineCount: 1
                            }

                            StyledText {
                                width: parent.width
                                text: hitRow.modelData.snippet || ""
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceVariantText
                                elide: Text.ElideRight
                                maximumLineCount: 2
                                wrapMode: Text.WordWrap
                            }

                            StyledText {
                                width: parent.width
                                text: modal.roleLabel(hitRow.modelData.role) + " · " + modal.whenLabel(hitRow.modelData.createdAt)
                                font.pixelSize: Theme.fontSizeSmall - 1
                                color: Theme.surfaceVariantText
                                opacity: 0.8
                                elide: Text.ElideRight
                                maximumLineCount: 1
                            }
                        }

                        // StateLayer is itself the MouseArea: hoverEnabled, pointing-hand cursor,
                        // hover overlay and press ripple all come with it. Splitting hover and
                        // click across two items is what silently broke an earlier version — a
                        // cursorShape on a MouseArea without hoverEnabled never gets hover, and
                        // two hit areas can drift apart into "clickable but never lit".
                        StateLayer {
                            stateColor: Theme.surfaceText
                            onClicked: {
                                modal.selectedIndex = hitRow.index;
                                modal.openHit(hitRow.index);
                            }
                        }
                    }
                }
            }
        }
    }
}
