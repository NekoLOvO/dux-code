.pragma library

var RE_FENCE = /^```\s*(\S*)\s*$/
var RE_HR    = /^(-{3,}|\*{3,}|_{3,})$/
var RE_HEAD  = /^(#{1,6})\s+(.*)$/
var RE_QUOTE = /^>\s?(.*)$/
var RE_UL    = /^[-*+]\s+(.*)$/
var RE_OL    = /^\d+[.)]\s+(.*)$/

function isBlank(s) { return /^\s*$/.test(s) }

function finalizeTool(b) {
    try {
        b.args = JSON.parse(b.code && b.code.length ? b.code : "{}")
    } catch (e) {
        b.args = { _raw: b.code }
    }
}

// markdown text -> block array
// { type: p | h1..h3 | ul | ol | quote | code | tool | hr }
function parse(text) {
    var src = (text === undefined || text === null) ? "" : String(text)
    var lines = src.split("\n")
    var blocks = []
    var cur = null

    function flush() { if (cur) { blocks.push(cur); cur = null } }

    for (var i = 0; i < lines.length; i++) {
        var raw = lines[i]
        var t = raw.trim()

        // Inside a fenced code / tool block.
        if (cur && (cur.type === "code" || cur.type === "tool")) {
            if (t.indexOf("```") === 0) {
                cur.closed = true
                if (cur.type === "tool") { finalizeTool(cur); cur.status = "done" }
                flush()
            } else {
                cur.code += (cur.code.length > 0 ? "\n" : "") + raw
            }
            continue
        }

        // Opening fence.
        var m = RE_FENCE.exec(t)
        if (m) {
            flush()
            var info = m[1] || ""
            var isTool = info.indexOf("tool:") === 0
            cur = {
                type: isTool ? "tool" : "code",
                lang: isTool ? info.slice(5) : info,
                code: "",
                closed: false,
                status: isTool ? "running" : "",
                args: null
            }
            continue
        }

        if (isBlank(raw)) { flush(); continue }
        if (RE_HR.test(t)) { flush(); blocks.push({ type: "hr" }); continue }

        var h = RE_HEAD.exec(t)
        if (h) {
            flush()
            blocks.push({ type: "h" + Math.min(h[1].length, 3), text: h[2] })
            continue
        }

        var q = RE_QUOTE.exec(t)
        if (q) {
            if (cur && cur.type === "quote") cur.text += "\n" + q[1]
            else { flush(); cur = { type: "quote", text: q[1] } }
            continue
        }

        var ul = RE_UL.exec(t)
        if (ul) {
            if (cur && cur.type === "ul") cur.items.push(ul[1])
            else { flush(); cur = { type: "ul", items: [ul[1]] } }
            continue
        }

        var ol = RE_OL.exec(t)
        if (ol) {
            if (cur && cur.type === "ol") cur.items.push(ol[1])
            else { flush(); cur = { type: "ol", items: [ol[1]] } }
            continue
        }

        if (cur && cur.type === "p") cur.text += (cur.text.length > 0 ? "\n" : "") + t
        else { flush(); cur = { type: "p", text: t } }
    }

    flush()
    return blocks
}

// Serialize a block back to Markdown for Qt's MarkdownText.
function toMarkdown(b) {
    if (!b) return ""
    if (b.type === "ul") {
        return b.items.map(function (it) { return "- " + it }).join("\n")
    }
    if (b.type === "ol") {
        return b.items.map(function (it, i) { return (i + 1) + ". " + it }).join("\n")
    }
    if (b.type === "quote") {
        return b.text.split("\n").map(function (l) { return "> " + l }).join("\n")
    }
    return b.text ? b.text : ""
}
