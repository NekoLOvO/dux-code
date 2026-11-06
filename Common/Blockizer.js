.pragma library

var RE_FENCE = /^```\s*(\S*)\s*$/
var RE_HR    = /^(-{3,}|\*{3,}|_{3,})$/
var RE_HEAD  = /^(#{1,6})\s+(.*)$/
var RE_QUOTE = /^>\s?(.*)$/
var RE_UL    = /^[-*+]\s+(.*)$/
var RE_OL    = /^\d+[.)]\s+(.*)$/

var RE_PIPE  = /\|/
// One cell of a table's delimiter row: dashes, optionally colon-aligned at either end.
var RE_DELIM_CELL = /^:?-+:?$/

function isBlank(s) { return /^\s*$/.test(s) }

function finalizeTool(b) {
    try {
        b.args = JSON.parse(b.code && b.code.length ? b.code : "{}")
    } catch (e) {
        b.args = { _raw: b.code }
    }
}

// markdown text -> block array
// { type: p | h1..h3 | ul | ol | quote | table | code | tool | hr }
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

        // A table: this line is the header row and the next one is the delimiter row. Checked before the
        // paragraph branch, or the header and every row below it fold into one paragraph of pipes.
        if (RE_PIPE.test(t) && isTableDelimiter(lines[i + 1])) {
            var head = tableCells(t)
            var align = tableAlign(tableCells(lines[i + 1]))
            // The two rows have to agree on the column count; otherwise this was never a table (a
            // paragraph that happens to contain a pipe, followed by a rule).
            if (align.length === head.length) {
                flush()
                var rows = []
                var r = i + 2
                for (; r < lines.length; r++) {
                    var rowText = lines[r].trim()
                    if (rowText === "" || !RE_PIPE.test(rowText))
                        break
                    var cells = tableCells(rowText)
                    // The delimiter row decides the width: a short row loses nothing by being padded,
                    // and a long one must not push a column the header never declared.
                    while (cells.length < head.length)
                        cells.push("")
                    rows.push(cells.slice(0, head.length))
                }
                blocks.push({ type: "table", header: head, align: align, rows: rows })
                i = r - 1
                continue
            }
        }

        if (cur && cur.type === "p") cur.text += (cur.text.length > 0 ? "\n" : "") + t
        else { flush(); cur = { type: "p", text: t } }
    }

    flush()
    return blocks
}

// ---------- tables ----------
//
// A GitHub pipe table. Not "prose" in the Markdown sense, but text the user reads and copies, so it is
// rendered inside the same TextEdit as the paragraphs around it (see PROSE): Qt's selection then runs
// through a table instead of stopping at its edge.
//
// Qt's rich text draws a grid only from the *table's* border plus border-collapse. A border on a cell
// paints nothing on its own, and there is no per-side border at all — measured on Qt 6, so: every line
// is the same weight and colour (no rule under the header only), and a cell background is inset by
// cellpadding, which rules out a full-bleed header band. The header is told apart by weight alone. Do
// not "improve" that without a screenshot to check it.

// Cells of one pipe-table row.
//
// Not a plain split("|"): `\|` is a literal pipe inside a cell (a shell pipeline in a table is exactly
// where that shows up), and the leading / trailing pipe is the row's own fence, not an empty cell.
function tableCells(line) {
    var s = String(line === undefined || line === null ? "" : line).trim()
    if (s.charAt(0) === "|")
        s = s.slice(1)
    if (s.length > 0 && s.charAt(s.length - 1) === "|" && s.charAt(s.length - 2) !== "\\")
        s = s.slice(0, -1)

    var cells = []
    var cur = ""
    for (var i = 0; i < s.length; i++) {
        var ch = s.charAt(i)
        var next = s.charAt(i + 1)
        if (ch === "\\" && (next === "|" || next === "\\")) {
            cur += next
            i++
            continue
        }
        if (ch === "|") {
            cells.push(cur.trim())
            cur = ""
            continue
        }
        cur += ch
    }
    cells.push(cur.trim())
    return cells
}

// The delimiter row decides where each column's text sits: `:--` left, `--:` right, `:-:` centre.
function tableAlign(cells) {
    var out = []
    for (var i = 0; i < cells.length; i++) {
        var c = cells[i]
        var head = c.charAt(0) === ":"
        var tail = c.charAt(c.length - 1) === ":"
        out.push(head && tail ? "center" : tail ? "right" : "left")
    }
    return out
}

// Is this the `| --- | :--: |` line under a header row?
//
// The pipe is required, not just tolerated: a single-column table without one is indistinguishable
// from a setext heading (`a` over `---`), and guessing wrong costs a line of the answer.
function isTableDelimiter(line) {
    var t = String(line === undefined || line === null ? "" : line).trim()
    if (t.indexOf("-") < 0 || !RE_PIPE.test(t))
        return false
    var cells = tableCells(t)
    for (var i = 0; i < cells.length; i++) {
        if (!RE_DELIM_CELL.test(cells[i]))
            return false
    }
    return true
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
var PROSE = { p: true, h1: true, h2: true, h3: true, ul: true, ol: true, quote: true, table: true }

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

    if (t === "table")
        return tableHtml(b, st, first)

    return "<p style=\"" + style(
        "margin-top:" + (first ? 0 : st.gap) + "px;font-size:" + st.base + "px;color:" + st.text) + "\">"
        + inlineHtml(String(b.text || "").split("\n").join(" "), st) + "</p>"
}

// One table as HTML.
//
// Every cell states its alignment, because the delimiter row is where `---:` and `:-:` live and Qt's
// own default would put every column on the left edge. No width is set either — Qt sizes the columns
// from their contents and wraps inside a wide cell, which is what a Markdown renderer is expected to
// do; a table stretched to the full column would put two words and a lot of nothing in each cell.
function tableHtml(b, st, first) {
    var head = b.header || []
    var rows = b.rows || []
    var align = b.align || []

    function edge(i) { return align[i] || "left" }

    var cell = "font-family:" + st.font + ";font-size:" + st.base + "px;color:" + st.text + ";"
    var html = '<table cellspacing="0" cellpadding="' + st.cellPad + '" style="margin:0;'
        + (first ? "" : "margin-top:" + st.gap + "px;")
        + "border:1px solid " + st.tableLine + ";border-collapse:collapse;\">"

    var tr = "<tr>"
    for (var c = 0; c < head.length; c++)
        tr += "<th align=\"" + edge(c) + "\" style=\"" + cell + "font-weight:600;\">"
            + inlineHtml(head[c], st) + "</th>"
    html += tr + "</tr>"

    for (var r = 0; r < rows.length; r++) {
        tr = "<tr>"
        var row = rows[r] || []
        for (var i = 0; i < head.length; i++) {
            var v = row[i] === undefined ? "" : row[i]
            tr += "<td align=\"" + edge(i) + "\" style=\"" + cell + "\">" + inlineHtml(v, st) + "</td>"
        }
        html += tr + "</tr>"
    }

    return html + "</table>"
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
// Lists, quotes and tables carry no .text of their own, so they are rebuilt from their parts.
function proseCopy(blk) {
    if (!blk)
        return ""
    var items = blk.type === "prose" ? (blk.items || []) : [blk]
    var out = []
    for (var i = 0; i < items.length; i++) {
        var b = items[i]
        if (!b) continue
        if (b.type === "ul" || b.type === "ol" || b.type === "quote" || b.type === "table")
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
    if (b.type === "table") {
        // The delimiter row keeps each column's alignment, so a copy pastes back the table it came
        // from rather than a left-aligned one.
        var align = b.align || []
        var marks = []
        for (var i = 0; i < (b.header || []).length; i++) {
            var a = align[i] || "left"
            marks.push(a === "center" ? ":---:" : a === "right" ? "---:" : "---")
        }
        var lines = [tableRow(b.header), tableRow(marks)]
        for (var r = 0; r < (b.rows || []).length; r++)
            lines.push(tableRow(b.rows[r]))
        return lines.join("\n")
    }
    return b.text ? b.text : ""
}

// A table row back to Markdown, one cell per column, pipes escaped.
function tableRow(cells) {
    var out = []
    var list = cells || []
    for (var i = 0; i < list.length; i++)
        out.push(" " + String(list[i] === undefined ? "" : list[i]).replace(/\|/g, "\\|") + " ")
    return "|" + out.join("|") + "|"
}
