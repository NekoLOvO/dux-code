pragma Singleton
import QtQuick
import qs.Services

// Models — data for the model menu on the main page.
//
// Holds only the models enabled in settings, from the daemon's info. Ids are global
// ("provider:model", e.g. deepseek:deepseek-chat), so switching provider keeps sessions.
QtObject {
    id: root

    readonly property var available: Backend.models
    readonly property bool isEmpty: available.length === 0

    // Currently selected model. The daemon's defaultModel is the only source; no local
    // override, so the value is not stored twice.
    readonly property string currentId: {
        if (Backend.defaultModel !== "" && has(Backend.defaultModel))
            return Backend.defaultModel;
        return available.length > 0 ? available[0].id : "";
    }

    readonly property var current: {
        for (let i = 0; i < available.length; i++) {
            if (available[i].id === currentId)
                return available[i];
        }
        return available.length > 0 ? available[0] : null;
    }

    // A model can be enabled and still be unusable without a provider key.
    readonly property bool currentReady: {
        if (!current)
            return false;
        if (current.ready === false)
            return false;
        for (let i = 0; i < Backend.providers.length; i++) {
            if (Backend.providers[i].id === current.provider)
                return Backend.providers[i].hasKey === true;
        }
        return false;
    }

    function has(id) {
        for (let i = 0; i < available.length; i++) {
            if (available[i].id === id)
                return true;
        }
        return false;
    }

    function select(id) {
        if (!id || id === currentId)
            return;
        Backend.setDefaultModel(id);
    }

    function label(id) {
        for (let i = 0; i < available.length; i++) {
            if (available[i].id === id)
                return available[i].label || available[i].id;
        }
        return id || "";
    }
}
