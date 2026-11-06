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

// ---------- prose runs ----------
//
// Every block used to be its own TextEdit, which meant a selection could never leave one paragraph.
// The daemon has no say in this: it is a rendering limit of "one document per block". Prose (paragraphs,
// headings, lists, quotes) is grouped into a single run so one TextEdit holds all of it and Qt's own
// selection spans the whole thing. Code and tool cards stay separate — they are widgets, not text.
//
// Rendered as HTML rather than Markdown because MarkdownText takes Qt's fixed heading scale (2x / 1.5x
// / 1.1875x of the body), which is nothing like the Theme's 20 / 16 / 15. RichText lets every size,
// color and margin come from Theme instead of from Qt's defaults.
var PROSE = { p: true, h1: true, h2: true, h3: true, ul: true, ol: true, quote: true }

function isProse(b) { return !!b && PROSE[b.type] === true }

// Consecutive prose blocks become one { type: "prose", items: [...] }.
function groupProse(blocks) {
    var out = []
    var run = null
    for (var i = 0; i < blocks.length; i++) {
        var b = blocks[i]
        if (isProse(b)) {
            if (!run) {
                run = { type: "prose", items: [] }
                out.push(run)
            }
            run.items.push(b)
        } else {
            run = null
            out.push(b)
        }
    }
    return out
}

function escapeHtml(s) {
    return String(s === undefined || s === null ? "" : s)
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
}

// Inline Markdown inside one line of prose.
//
// Code spans are lifted out first: `**not bold**` inside backticks has to stay literal, and the
// placeholder keeps the later passes from touching it. The text is escaped before any of this, so the
// markers that survive are the ones the author actually typed.
function inlineHtml(src, st) {
    var t = escapeHtml(src)
    var spans = []

    t = t.replace(/`([^`]+)`/g, function (_, code) {
        spans.push(code)
        return "\u0000" + (spans.length - 1) + "\u0000"
    })

    // Links: only the schemes the rest of the app is willing to open. Anything else keeps its label
    // and loses the link, rather than handing an unknown scheme to xdg-open.
    t = t.replace(/\[([^\]]*)\]\(([^)\s]+)\)/g, function (whole, label, url) {
        if (!/^(https?:\/\/|mailto:)/i.test(url))
            return label
        return '<a href="' + url + '">' + label + "</a>"
    })

    t = t.replace(/\*\*([^\s*](?:[^*]*[^\s*])?)\*\*/g, "<b>$1</b>")
    t = t.replace(/__([^\s_](?:[^_]*[^\s_])?)__/g, "<b>$1</b>")
    // Emphasis follows CommonMark's flanking rule, loosely: the marker may not sit next to
    // whitespace on the inside. Without it `2 * 3 = 6` turns half the sentence italic, and prose
    // about pointers (`*char`, `**kwargs`) is exactly what a coding agent writes.
    t = t.replace(/(^|[^*\w])\*([^\s*](?:[^*]*[^\s*])?)\*(?=[^*\w]|$)/g, "$1<i>$2</i>")
    t = t.replace(/(^|[^_\w])_([^\s_](?:[^_]*[^\s_])?)_(?=[^_\w]|$)/g, "$1<i>$2</i>")
    t = t.replace(/~~([^\s~](?:[^~]*[^\s~])?)~~/g, "<s>$1</s>")

    t = t.replace(/\u0000(\d+)\u0000/g, function (_, i) {
        return '<span style="font-family:' + st.mono + ";background-color:" + st.codeBg
            + ";color:" + st.codeFg + ';">' + spans[+i] + "</span>"
    })

    return t
}

// One block of a prose run as an HTML paragraph.
//
// margin:0 everywhere, then a top margin on everything but the first: Qt gives <p> a ~12px default
// that would double up with the gap the caller already spaces blocks with.
function blockHtml(b, st, first) {
    var t = b.type

    // Every branch builds the same base and only differs in type size, weight and color.
    function style(extra) {
        return "margin:0;font-family:" + st.font + ";" + extra
    }

    if (t === "h1" || t === "h2" || t === "h3") {
        var size = t === "h1" ? st.h1 : t === "h2" ? st.h2 : st.h3
        return "<p style=\"" + style(
            "margin-top:" + (first ? 0 : st.gap + st.headGap) + "px;" +
            "font-size:" + size + "px;font-weight:600;color:" + st.text) + "\">"
            + inlineHtml(b.text, st) + "</p>"
    }

    if (t === "quote") {
        return "<p style=\"" + style(
            "margin-top:" + (first ? 0 : st.gap) + "px;margin-left:" + st.quoteIndent + "px;" +
            "font-size:" + st.base + "px;color:" + st.quote) + "\">"
            + inlineHtml(String(b.text || "").split("\n").join(" "), st) + "</p>"
    }

    if (t === "ul" || t === "ol") {
        var tag = t === "ul" ? "ul" : "ol"
        var items = b.items || []
        var lis = []
        for (var i = 0; i < items.length; i++)
            lis.push("<li>" + inlineHtml(items[i], st) + "</li>")
        return "<" + tag + " style=\"" + style(
            "margin-top:" + (first ? 0 : st.gap) + "px;font-size:" + st.base + "px;color:" + st.text)
            + "\">" + lis.join("") + "</" + tag + ">"
    }

    return "<p style=\"" + style(
        "margin-top:" + (first ? 0 : st.gap) + "px;font-size:" + st.base + "px;color:" + st.text) + "\">"
        + inlineHtml(String(b.text || "").split("\n").join(" "), st) + "</p>"
}

// A prose run (or a lone prose block) as RichText for one TextEdit.
function toHtml(blk, st) {
    if (!blk)
        return ""
    var items = blk.type === "prose" ? (blk.items || []) : [blk]
    var out = []
    for (var i = 0; i < items.length; i++)
        out.push(blockHtml(items[i], st, i === 0))
    return out.join("")
}

// The Markdown a prose run came from, for "copy this block".
// Lists and quotes carry no .text of their own, so they are rebuilt from their parts.
function proseCopy(blk) {
    if (!blk)
        return ""
    var items = blk.type === "prose" ? (blk.items || []) : [blk]
    var out = []
    for (var i = 0; i < items.length; i++) {
        var b = items[i]
        if (!b) continue
        if (b.type === "ul" || b.type === "ol" || b.type === "quote")
            out.push(toMarkdown(b))
        else
            out.push(String(b.text || ""))
    }
    return out.join("\n\n")
}

// Serialize a block back to Markdown.
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
