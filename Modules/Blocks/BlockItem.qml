import QtQuick
import qs.Common
import qs.Modules.Blocks

// Dispatches by block type. While streaming only the trailing block changes, and finished
// blocks are not rebuilt (Repeater models an index, not an array).
Item {
    id: root

    property var blk: ({})
    // Subdued variant for thinking blocks: smaller text, secondary color.
    // Applies only to blocks that use this property (not ToolBlock/RuleBlock).
    property bool muted: false

    implicitHeight: loader.item ? loader.item.implicitHeight : 0

    Loader {
        id: loader
        width: root.width
        sourceComponent: {
            var t = root.blk ? root.blk.type : ""
            if (t === "code") return codeComp
            if (t === "tool") return toolComp
            if (t === "hr") return ruleComp
            // "prose" is a run of paragraphs/headings/lists/quotes/tables sharing one TextEdit; a lone
            // prose block (the streaming fallback) lands here too.
            return textComp
        }
        onLoaded: {
            if (!item)
                return;
            item.blk = Qt.binding(function () { return root.blk })
            if (item.muted !== undefined)
                item.muted = Qt.binding(function () { return root.muted })
        }
    }

    Component { id: textComp; TextBlock {} }
    Component { id: codeComp; CodeBlock {} }
    Component { id: toolComp; ToolBlock {} }
    Component { id: ruleComp; RuleBlock {} }
}
