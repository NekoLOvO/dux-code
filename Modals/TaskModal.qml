import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.DCommon.Widgets

// Scheduled tasks.
//
// One window holds both pages: the list (view / run) and the editor (new / edit). Creating a
// task almost always starts from the list, so a second window would only add a stack.
FloatingWindow {
    id: modal

    property bool editing: false
    property string editId: ""
    property bool busy: false
    property string errorText: ""

    // Draft the editor is bound to.
    property string name: ""
    property string prompt: ""
    property string scheduleKind: "daily"
    property string atTime: "09:00"
    property int intervalMinutes: 60
    property int weekday: 1
    property string workDir: ""
    property string modelId: ""
    property bool autoApprove: false
    property bool taskEnabled: true

    function reset() {
        editId = "";
        name = "";
        prompt = "";
        scheduleKind = "daily";
        atTime = "09:00";
        intervalMinutes = 60;
        weekday = 1;
        workDir = "";
        modelId = "";
        autoApprove = false;
        taskEnabled = true;
        busy = false;
        errorText = "";
    }

    function show() {
        reset();
        editing = false;
        visible = true;
        Tasks.reload();
    }

    function hide() {
        visible = false;
    }

    function startNew() {
        reset();
        editing = true;
    }

    function startEdit(t) {
        reset();
        editId = t.id || "";
        name = t.name || "";
        prompt = t.prompt || "";
        scheduleKind = t.kind || "manual";
        atTime = t.at || "09:00";
        intervalMinutes = t.minutes || 60;
        weekday = t.weekday === undefined ? 1 : t.weekday;
        workDir = t.workingDirectory || "";
        modelId = t.model || "";
        autoApprove = t.autoApprove === true;
        taskEnabled = t.enabled !== false;
        editing = true;
    }

    function save() {
        if (busy)
            return;
        const n = String(name).trim();
        const p = String(prompt).trim();
        if (n === "") {
            errorText = I18n.tr("Give the task a name.", "task form error when the name is empty");
            return;
        }
        if (p === "") {
            errorText = I18n.tr("A task needs a prompt to run.", "task form error when the prompt is empty");
            return;
        }
        busy = true;
        errorText = "";
        Tasks.save({
            "id": editId,
            "name": n,
            "prompt": p,
            "kind": scheduleKind,
            "at": atTime,
            "minutes": intervalMinutes,
            "weekday": weekday,
            "workingDirectory": String(workDir).trim(),
            "model": modelId,
            "autoApprove": autoApprove,
            "enabled": taskEnabled
        }, (res, err) => {
            busy = false;
            if (err) {
                errorText = err;
                return;
            }
            editing = false;
            ToastService.info(editId === "" ? I18n.tr("Task added", "toast after creating a task") : I18n.tr("Task saved", "toast after saving a task"));
        });
    }

    function removeCurrent() {
        if (editId === "")
            return;
        busy = true;
        Tasks.remove(editId, (res, err) => {
            busy = false;
            if (err) {
                errorText = err;
                return;
            }
            editing = false;
        });
    }

    function runTask(id) {
        Tasks.run(id);
    }

    function statusText(t) {
        if (t.running)
            return I18n.tr("Running…", "task row status while it is executing");
        if (!t.lastRunAt)
            return I18n.tr("Never run", "task row when the task has never executed");
        const when = Qt.formatDateTime(new Date(t.lastRunAt), "MM-dd HH:mm");
        if (t.lastStatus === "failed")
            return I18n.tr("Failed %1", "task row when the last run failed").arg(when);
        if (t.lastStatus === "cancelled")
            return I18n.tr("Cancelled %1", "task row when the last run was cancelled").arg(when);
        return I18n.tr("Ran %1", "task row when the last run succeeded").arg(when);
    }

    title: I18n.tr("Tasks", "task modal window title")
    minimumSize: Qt.size(560, 460)
    implicitWidth: Math.max(minimumSize.width, Theme.modalWidth(parentWindow, screen, 720))
    implicitHeight: Math.max(minimumSize.height, Math.min(screen ? screen.height - 80 : 900, 620))
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

            // --- list page ---
            Column {
                anchors.fill: parent
                anchors.margins: Theme.spacingL
                spacing: Theme.spacingM
                visible: !modal.editing

                StyledText {
                    width: parent.width
                    text: I18n.tr("A task is a prompt that runs on a schedule. Each run gets its own conversation, so you can read what it did — or keep asking.", "task modal intro")
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                    wrapMode: Text.WordWrap
                }

                StyledText {
                    visible: Tasks.tasks.length === 0
                    width: parent.width
                    text: I18n.tr("No tasks yet", "task modal empty state")
                    font.pixelSize: Theme.fontSizeSmall
                    color: Theme.surfaceVariantText
                }

                DListView {
                    width: parent.width
                    height: parent.height - y - newButton.height - Theme.spacingM
                    clip: true
                    spacing: Theme.groupedListGap
                    model: Tasks.tasks
                    showScrollBar: true
                    boundsBehavior: Flickable.StopAtBounds

                    delegate: StyledRect {
                        id: taskRow

                        required property int index
                        required property var modelData

                        width: ListView.view ? ListView.view.width : 0
                        height: rowColumn.implicitHeight + Theme.spacingM * 2
                        radius: Theme.cornerRadiusM
                        color: Theme.surfaceContainerHigh

                        Column {
                            id: rowColumn

                            anchors.left: parent.left
                            anchors.right: rowButtons.left
                            anchors.verticalCenter: parent.verticalCenter
                            anchors.leftMargin: Theme.spacingM
                            anchors.rightMargin: Theme.spacingS
                            spacing: Theme.spacingXS

                            StyledText {
                                width: parent.width
                                text: taskRow.modelData.name || ""
                                font.pixelSize: Theme.fontSizeSmall + 1
                                font.weight: Font.DemiBold
                                color: taskRow.modelData.enabled ? Theme.surfaceText : Theme.surfaceVariantText
                                elide: Text.ElideRight
                                maximumLineCount: 1
                            }

                            StyledText {
                                width: parent.width
                                text: {
                                    // A disabled task never runs on its own, so say that instead of the schedule.
                                    if (!taskRow.modelData.enabled)
                                        return I18n.tr("Paused", "task row when the task is disabled");
                                    return Tasks.scheduleLabel(taskRow.modelData);
                                }
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceVariantText
                                elide: Text.ElideRight
                                maximumLineCount: 1
                            }

                            StyledText {
                                width: parent.width
                                text: modal.statusText(taskRow.modelData)
                                font.pixelSize: Theme.fontSizeSmall - 1
                                color: taskRow.modelData.lastStatus === "failed" ? Theme.error : Theme.surfaceVariantText
                                opacity: 0.85
                                elide: Text.ElideRight
                                maximumLineCount: 1
                            }
                        }

                        Row {
                            id: rowButtons

                            anchors.right: parent.right
                            anchors.rightMargin: Theme.spacingS
                            anchors.verticalCenter: parent.verticalCenter
                            spacing: Theme.spacingXS

                            DIconButton {
                                iconName: "play_arrow"
                                iconSize: Theme.iconSize
                                onClicked: modal.runTask(taskRow.modelData.id)
                            }

                            DIconButton {
                                iconName: "edit"
                                iconSize: Theme.iconSize
                                onClicked: modal.startEdit(taskRow.modelData)
                            }
                        }
                    }
                }

                DButton {
                    id: newButton

                    width: parent.width
                    text: I18n.tr("New task", "task modal new button")
                    iconName: "add"
                    buttonHeight: Theme.buttonHeightS
                    onClicked: modal.startNew()
                }
            }

            // --- editor page ---
            DFlickable {
                anchors.fill: parent
                anchors.margins: Theme.spacingL
                clip: true
                contentWidth: width
                contentHeight: formColumn.implicitHeight
                visible: modal.editing

                Column {
                    id: formColumn

                    width: parent.width
                    spacing: Theme.spacingM

                    DTextField {
                        width: parent.width
                        outlined: true
                        labelText: I18n.tr("Name", "task form name label")
                        placeholderText: I18n.tr("Morning digest", "task form name placeholder")
                        text: modal.name
                        onTextChanged: modal.name = text
                    }

                    DTextEdit {
                        width: parent.width
                        height: 110
                        placeholderText: I18n.tr("What should it do? This is sent as the first message.", "task form prompt placeholder")
                        text: modal.prompt
                        wrapMode: TextEdit.Wrap
                        onTextChanged: modal.prompt = text
                    }

                    StyledText {
                        width: parent.width
                        text: I18n.tr("Schedule", "task form schedule label")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                    }

                    DDropdown {
                        width: parent.width
                        dropdownWidth: parent.width
                        options: [
                            I18n.tr("Manual only", "task schedule: never runs on its own"),
                            I18n.tr("Every day", "task schedule kind: daily"),
                            I18n.tr("Every week", "task schedule kind: weekly"),
                            I18n.tr("Every N minutes", "task schedule kind: fixed interval")
                        ]
                        optionValues: ["manual", "daily", "weekly", "every"]
                        currentValue: modal.scheduleKind
                        onValueChanged: value => modal.scheduleKind = value
                    }

                    Row {
                        width: parent.width
                        spacing: Theme.spacingM
                        visible: modal.scheduleKind === "daily" || modal.scheduleKind === "weekly"

                        DTextField {
                            width: (parent.width - Theme.spacingM) / 2
                            outlined: true
                            labelText: I18n.tr("Time (HH:MM)", "task form time label")
                            placeholderText: "09:00"
                            text: modal.atTime
                            onTextChanged: modal.atTime = text
                        }

                        Column {
                            width: (parent.width - Theme.spacingM) / 2
                            visible: modal.scheduleKind === "weekly"
                            spacing: Theme.spacingXS

                            StyledText {
                                width: parent.width
                                text: I18n.tr("Weekday", "task form weekday label")
                                font.pixelSize: Theme.fontSizeSmall
                                color: Theme.surfaceVariantText
                            }

                            DDropdown {
                                width: parent.width
                                dropdownWidth: parent.width
                                options: [0, 1, 2, 3, 4, 5, 6].map(i => Tasks.weekdayName(i))
                                optionValues: ["0", "1", "2", "3", "4", "5", "6"]
                                currentValue: String(modal.weekday)
                                onValueChanged: value => modal.weekday = parseInt(value, 10)
                            }
                        }
                    }

                    DTextField {
                        width: parent.width
                        visible: modal.scheduleKind === "every"
                        outlined: true
                        labelText: I18n.tr("Minutes", "task form interval label")
                        placeholderText: "60"
                        text: String(modal.intervalMinutes)
                        onTextChanged: {
                            const n = parseInt(text, 10);
                            if (!isNaN(n))
                                modal.intervalMinutes = n;
                        }
                    }

                    DTextField {
                        width: parent.width
                        outlined: true
                        labelText: I18n.tr("Working directory", "task form working directory label")
                        placeholderText: I18n.tr("Optional — empty uses the daemon default", "task form working directory hint")
                        text: modal.workDir
                        onTextChanged: modal.workDir = text
                    }

                    StyledText {
                        width: parent.width
                        text: I18n.tr("Model", "task form model label")
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.surfaceVariantText
                    }

                    DDropdown {
                        width: parent.width
                        dropdownWidth: parent.width
                        options: [I18n.tr("Use default", "task form model option")].concat(Models.available.map(m => m.label))
                        optionValues: [""].concat(Models.available.map(m => m.id))
                        currentValue: modal.modelId
                        onValueChanged: value => modal.modelId = value
                    }

                    DToggle {
                        width: parent.width
                        text: I18n.tr("Auto-approve tools", "task form toggle label")
                        description: I18n.tr("Tasks run with no window open, so nobody can answer a permission prompt. With this off the task can only read; turn it on and it may write and run commands unattended.", "task form auto-approve description")
                        checked: modal.autoApprove
                        onToggled: checked => modal.autoApprove = checked
                    }

                    DToggle {
                        width: parent.width
                        text: I18n.tr("Enabled", "task form enabled label")
                        description: I18n.tr("Off keeps the task but stops it from running on its own.", "task form enabled description")
                        checked: modal.taskEnabled
                        onToggled: checked => modal.taskEnabled = checked
                    }

                    StyledText {
                        width: parent.width
                        visible: modal.errorText !== ""
                        text: modal.errorText
                        font.pixelSize: Theme.fontSizeSmall
                        color: Theme.error
                        wrapMode: Text.WordWrap
                    }

                    Row {
                        width: parent.width
                        spacing: Theme.spacingS

                        DButton {
                            text: I18n.tr("Cancel", "task form cancel button")
                            buttonHeight: Theme.buttonHeightS
                            onClicked: modal.editing = false
                        }

                        DButton {
                            visible: modal.editId !== ""
                            text: I18n.tr("Delete", "task form delete button")
                            buttonHeight: Theme.buttonHeightS
                            variant: "tonal"
                            danger: true
                            onClicked: modal.removeCurrent()
                        }

                        Item {
                            width: parent.width - 200
                            height: 1
                        }

                        DButton {
                            text: modal.busy ? I18n.tr("Saving…", "task form while saving") : I18n.tr("Save", "task form save button")
                            buttonHeight: Theme.buttonHeightS
                            enabled: !modal.busy
                            onClicked: modal.save()
                        }
                    }
                }
            }
        }
    }
}
