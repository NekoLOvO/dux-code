pragma Singleton

import QtQuick

// One selection at a time, across the whole transcript.
//
// A reply is drawn as several TextEdits — a prose run per stretch, a code card per fence — and Qt
// gives every one of them a selection of its own. Nothing ever cleared the others, so picking out a
// sentence in one paragraph and then a sentence in the next left both lit up: two selections that
// read as one, and a copy button that no longer had an obvious subject.
//
// Within a single TextEdit Qt already behaves the way a reader expects — selecting again replaces
// the selection — so this only has to cover the boundaries between them. The blocks announce
// themselves here; nothing walks the scene graph.
//
// A plain click counts as a selection too, of nothing: the block the click landed in claims, so the
// highlight does not linger in a paragraph the user has already left. That is keyed off focus rather
// than the cursor, because the cursor also moves when a streaming reply rewrites its own text, and
// that must never pull the selection away from something further up the transcript.
QtObject {
    id: root

    // The editor whose selection is the live one, or null.
    property var owner: null

    // True while an editor is being cleared.
    //
    // Deselecting moves that editor's cursor, which fires the very signals that ask to claim, and
    // without this the block that just lost the selection would immediately take it back.
    property bool _clearing: false

    // Take the selection over, dropping whoever had it.
    function claim(edit) {
        if (!edit || _clearing || owner === edit)
            return;
        const previous = owner;
        owner = edit;
        if (!previous)
            return;
        _clearing = true;
        try {
            previous.deselect();
        } catch (e) {
            // The block is gone — a reparse or a session switch destroyed it along with its
            // selection, which is the state we wanted anyway.
        }
        _clearing = false;
    }

    // The owner went away. Called from the block's own destruction, so the next claim does not
    // reach into a dead object.
    function release(edit) {
        if (owner === edit)
            owner = null;
    }
}
