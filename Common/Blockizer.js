.pragma library

// Markdown -> blocks -> the HTML one TextEdit draws.
//
// A model writes CommonMark with GitHub's extensions (tables, task lists, strikethrough), so this
// understands that dialect and no more. Two rules shape everything here:
//
//   - everything the user can read is prose, and prose is ONE TextEdit per run (see groupProse), so
//     structure has to survive as HTML inside that document rather than as widgets;
//   - a thing Qt's rich text cannot draw is not "implemented" — the target is measured, not assumed.
//     Block elements have no borders (so a quote is a tinted block, not a bar) and a cell background
//     covers only its first line.

var RE_FENCE = /^```\s*(\S*)\s*$/
var RE_HR    = /^(-{3,}|\*{3,}|_{3,})$/
var RE_HEAD  = /^(#{1,6})\s+(.*)$/
var RE_QUOTE = /^>\s?(.*)$/
var RE_PIPE  = /\|/
// One cell of a table's delimiter row: dashes, optionally colon-aligned at either end.
var RE_DELIM_CELL = /^:?-+:?$/
// A link reference definition: `[label]: url "optional title"`. Not a footnote (`[^x]: ...`).
var RE_REF_DEF  = /^\[([^\]]+)\]:\s*(\S+)/
var RE_FOOT_DEF = /^\[\^([^\]]+)\]:\s*(.*)$/
// The `[x]` / `[ ]` prefix of a task item.
var RE_TASK = /^\[([ xX])\]\s+/
// A line that is nothing but an image. Inline images stay inline (see the inline pass); this is the
// standalone case, which gets a widget.
var RE_IMAGE_LINE = /^!\[([^\]]*)\]\(\s*([^)\s]+)(?:\s+"([^"]*)")?\s*\)$/
var RE_IMAGE_REF  = /^!\[([^\]]*)\]\[([^\]]*)\]$/
// Inline HTML this renderer draws, mapped to tags Qt's rich text understands.
var RE_INLINE_TAG = /<(\/?)(u|b|strong|i|em|s|del|strike|code|sub|sup|mark|kbd|br)\s*\/?>/gi
// Block-level HTML that only adds structure we already express: dropped, content kept.
var RE_HTML_NOISE = /^<\/?(details|summary|div|section|article|table|tbody|thead|tr|td|th|p|ul|ol|li)\b[^>]*>$/i
var RE_HTML_TAG_ANY = /<\/?[a-zA-Z][^>]*>/g

// Block markers, in the order they are tried.
var RE_UL = /^[-*+]\s+(.*)$/
var RE_OL = /^\d{1,9}[.)]\s+(.*)$/

function isBlank(s) { return /^\s*$/.test(s) }

// Leading whitespace as a column count. A tab is four columns: this is a Markdown indentation
// measure, not a screen position.
function indentOf(s) {
    var col = 0
    for (var i = 0; i < s.length; i++) {
        var ch = s.charAt(i)
        if (ch === " ") col += 1
        else if (ch === "\t") col += 4
        else break
    }
    return col
}

function stripIndent(s, n) {
    var col = 0, i = 0
    for (; i < s.length && col < n; i++) {
        var ch = s.charAt(i)
        if (ch === "\t") col += 4
        else if (ch === " ") col += 1
        else break
    }
    return s.slice(i)
}

function finalizeTool(b) {
    try {
        b.args = JSON.parse(b.code && b.code.length ? b.code : "{}")
    } catch (e) {
        b.args = { _raw: b.code }
    }
}

// A list marker with the column its content starts at, or null.
function listMarker(t) {
    var m = RE_UL.exec(t)
    if (m) return { kind: "ul", rest: m[1], num: 0, width: t.length - m[1].length }
    m = RE_OL.exec(t)
    if (m) return { kind: "ol", rest: m[1], num: parseInt(t, 10), width: t.length - m[1].length }
    return null
}

// markdown text -> block array
// { type: p | h1..h6 | ul | ol | quote | table | footnotes | code | tool | hr }
function parse(text) {
    var src = (text === undefined || text === null) ? "" : String(text)
    var lines = src.split("\n")
    var ctx = { refs: {}, footnotes: [], footnoteSeen: {} }
    // The reference map carries the footnote labels as well, so `[^1]` can tell "defined" from
    // "the model wrote brackets" at render time.
    ctx.refs.__footnotes = ctx.footnoteSeen
    var blocks = parseBlocks(lines, 0, lines.length, ctx)
    // An image written in the reference form can name a definition further down; now that every
    // definition has been read, the ones that resolve get their URL and the rest fall back to prose.
    resolveImages(blocks, ctx.refs)
    // Definitions are collected while parsing, so they end up under the reply rather than inline
    // where the model happened to write them.
    if (ctx.footnotes.length > 0)
        blocks.push({ type: "footnotes", items: ctx.footnotes, refs: ctx.refs })
    return blocks
}

// Fill in the URL of every `![alt][ref]` image in `blocks`, recursively.
//
// A quote and a list item are read by their own parseBlocks call, so their blocks are a level down
// and a loop over the top level alone would leave those images with no URL — which the card draws as
// nothing at all.
function resolveImages(blocks, refs) {
    for (var i = 0; i < blocks.length; i++) {
        var b = blocks[i]
        if (b.type === "quote") {
            resolveImages(b.children || [], refs)
            continue
        }
        if (b.type === "ul" || b.type === "ol") {
            var items = b.items || []
            for (var k = 0; k < items.length; k++)
                resolveImages(items[k].children || [], refs)
            continue
        }
        if (b.type !== "image" || b.refId === undefined)
            continue
        var resolved = refs[b.refId]
        if (resolved) {
            b.url = resolved
        } else {
            // No definition for that label: leave the line as the text the model wrote rather than
            // drawing an empty card for a picture nobody can see.
            blocks[i] = { type: "p", text: b.raw, refs: b.refs }
        }
    }
}

// Blocks from `from` up to `to`.
//
// Every block carries the reference map, because `[text][ref]` is resolved when the HTML is built,
// long after the definition was read.
function parseBlocks(lines, from, to, ctx) {
    var blocks = []
    var i = from
    while (i < to) {
        var raw = lines[i]
        if (isBlank(raw)) { i++; continue }
        var t = raw.trim()

        // Fenced code (and our own `tool:` card).
        var fence = RE_FENCE.exec(t)
        if (fence) {
            var info = fence[1] || ""
            var isTool = info.indexOf("tool:") === 0
            var blk = {
                type: isTool ? "tool" : "code",
                lang: isTool ? info.slice(5) : info,
                code: "",
                closed: false,
                status: isTool ? "running" : "",
                args: null,
                refs: ctx.refs
            }
            i++
            for (; i < to; i++) {
                if (lines[i].trim().indexOf("```") === 0) { blk.closed = true; i++; break }
                blk.code += (blk.code.length > 0 ? "\n" : "") + stripIndent(lines[i], 0)
            }
            if (blk.type === "tool") { finalizeTool(blk); blk.status = "done" }
            blocks.push(blk)
            continue
        }

        if (RE_HR.test(t)) { blocks.push({ type: "hr", refs: ctx.refs }); i++; continue }

        var h = RE_HEAD.exec(t)
        if (h) {
            blocks.push({ type: "h" + Math.min(h[1].length, 6), text: h[2].trim(), refs: ctx.refs })
            i++
            continue
        }

        // Blockquote: gather the lines that belong to it, drop one `>`, and read the rest as blocks
        // again — that is what makes `> > x` a quote inside a quote.
        if (RE_QUOTE.test(t)) {
            var inner = []
            while (i < to) {
                var qline = lines[i]
                var qt = qline.trim()
                if (RE_QUOTE.test(qt)) { inner.push(qt.replace(/^> ?/, "")); i++; continue }
                if (isBlank(qline)) {
                    var qj = i + 1
                    while (qj < to && isBlank(lines[qj])) qj++
                    if (qj < to && RE_QUOTE.test(lines[qj].trim())) { inner.push(""); i = qj; continue }
                }
                break
            }
            blocks.push({ type: "quote", children: parseBlocks(inner, 0, inner.length, ctx), refs: ctx.refs })
            continue
        }

        // Table: this line is the header row and the next is the delimiter row.
        if (RE_PIPE.test(t) && i + 1 < to && isTableDelimiter(lines[i + 1])) {
            var head = tableCells(t)
            var align = tableAlign(tableCells(lines[i + 1]))
            if (align.length === head.length) {
                var rows = []
                i += 2
                for (; i < to; i++) {
                    var rowText = lines[i].trim()
                    if (rowText === "" || !RE_PIPE.test(rowText)) break
                    var cells = tableCells(rowText)
                    // The delimiter row decides the width: a short row loses nothing by being padded,
                    // and a long one must not push a column the header never declared.
                    while (cells.length < head.length) cells.push("")
                    rows.push(cells.slice(0, head.length))
                }
                blocks.push({ type: "table", header: head, align: align, rows: rows, refs: ctx.refs })
                continue
            }
        }

        // Lists, at whatever indentation this line sits: the item's own content is re-parsed from a
        // stripped copy, so nesting costs one recursion rather than a level counter.
        var marker = listMarker(t)
        if (marker) {
            var parsed = parseList(lines, i, to, ctx, marker)
            blocks.push(parsed.block)
            i = parsed.next
            continue
        }

        // A definition line is not prose. Reading it is the whole point: left as text it prints
        // `[ref1]: https://…` in the middle of the answer.
        var foot = RE_FOOT_DEF.exec(t)
        if (foot) {
            var body = foot[2]
            i++
            // An indented block under a definition belongs to it (CommonMark footnote shape). A
            // while loop, not a for: the blank-line branch jumps the index, and a `continue` in a for
            // would then skip the very line it jumped to.
            while (i < to) {
                if (isBlank(lines[i])) {
                    var fj = i + 1
                    while (fj < to && isBlank(lines[fj])) fj++
                    if (fj >= to || indentOf(lines[fj]) < 2) break
                    i = fj
                    continue
                }
                if (indentOf(lines[i]) < 2) break
                body += (body.length > 0 ? "\n" : "") + lines[i].trim()
                i++
            }
            if (!ctx.footnoteSeen[foot[1]]) {
                ctx.footnoteSeen[foot[1]] = true
                ctx.footnotes.push({ label: foot[1], text: body })
            }
            continue
        }

        var ref = RE_REF_DEF.exec(t)
        if (ref) {
            if (!ctx.refs[ref[1].toLowerCase()]) ctx.refs[ref[1].toLowerCase()] = ref[2]
            i++
            continue
        }

        // HTML that only restates structure we already have: `<details>`/`<summary>` cannot be drawn
        // (rich text has no folding), so the tags go and the text stays.
        if (RE_HTML_NOISE.test(t)) { i++; continue }
        var summary = /^<summary>(.*)<\/summary>$/i.exec(t)
        if (summary) { blocks.push({ type: "p", text: "**" + summary[1] + "**", refs: ctx.refs }); i++; continue }

        // A standalone image is a widget, not a paragraph: rich text can draw one inline, but only at
        // its natural size — measured, a 300 px image inside a 200 px field overflows instead of
        // shrinking, and `width` alone scales it up when the image is smaller than the field. A card can
        // cap both sides, keep the aspect ratio, and offer the alt text when the load fails.
        var image = RE_IMAGE_LINE.exec(t)
        if (image) {
            blocks.push({ type: "image", url: image[2], alt: image[1], title: image[3] || "", refs: ctx.refs })
            i++
            continue
        }
        var imageRef = RE_IMAGE_REF.exec(t)
        if (imageRef) {
            // The definition may come after the image, so the id is kept and resolved in parse()
            // once every definition has been read.
            blocks.push({
                type: "image",
                refId: String(imageRef[2] || imageRef[1]).toLowerCase(),
                alt: imageRef[1],
                raw: t,
                refs: ctx.refs
            })
            i++
            continue
        }

        // Paragraph: plain lines up to the next block. The line breaks survive into the block so a
        // hard break (two trailing spaces, or a trailing backslash) can still be honoured.
        var textLines = []
        for (; i < to; i++) {
            if (isBlank(lines[i])) break
            if (textLines.length > 0 && isBlockStart(lines, i, to)) break
            // Leading indentation goes; trailing spaces stay — two of them are a hard break.
            textLines.push(lines[i].replace(/^[ \t]+/, ""))
        }
        blocks.push({ type: "p", text: textLines.join("\n"), refs: ctx.refs })
    }
    return blocks
}

// Does this line start something other than a paragraph line?
function isBlockStart(lines, i, to) {
    var t = lines[i].trim()
    if (RE_FENCE.test(t) || RE_HEAD.test(t) || RE_HR.test(t) || RE_QUOTE.test(t)) return true
    if (listMarker(t)) return true
    if (RE_FOOT_DEF.test(t) || RE_REF_DEF.test(t)) return true
    if (RE_HTML_NOISE.test(t)) return true
    if (RE_IMAGE_LINE.test(t) || RE_IMAGE_REF.test(t)) return true
    if (RE_PIPE.test(t) && i + 1 < to && isTableDelimiter(lines[i + 1])) return true
    return false
}

// One list, starting at line i. Returns the block and where the list ends.
//
// A nested list is not a special case: a line indented past this item's content column is part of
// the item, and re-parsing it from the stripped copy finds the child list on its own.
function parseList(lines, i, to, ctx, firstMarker) {
    var indent = indentOf(lines[i])
    var block = {
        type: firstMarker.kind,
        start: firstMarker.kind === "ol" ? firstMarker.num : 1,
        items: [],
        refs: ctx.refs
    }

    while (i < to) {
        var raw = lines[i]
        if (isBlank(raw)) break
        var t = raw.trim()
        var marker = listMarker(t)
        if (!marker || indentOf(raw) !== indent || marker.kind !== block.type) break

        var col = indent + marker.width
        var item = { text: marker.rest, checked: null, children: [] }

        var task = RE_TASK.exec(item.text)
        if (task) {
            item.checked = task[1] !== " "
            item.text = item.text.slice(task[0].length)
        }
        i++

        // Lines belonging to this item: anything at or past its content column, plus lazy
        // continuation lines (a wrapped paragraph, which Markdown lets sit at any indent).
        var body = []
        while (i < to) {
            var line = lines[i]
            if (isBlank(line)) {
                var j = i + 1
                while (j < to && isBlank(lines[j])) j++
                if (j >= to) { i = j; break }
                var jmark = listMarker(lines[j].trim())
                if (indentOf(lines[j]) === indent && jmark && jmark.kind === block.type) { i = j; break }
                if (indentOf(lines[j]) >= col) { body.push(""); i = j; continue }
                break
            }
            var lineIndent = indentOf(line)
            if (lineIndent >= col) { body.push(stripIndent(line, col)); i++; continue }
            if (listMarker(line.trim())) break
            if (isBlockStart(lines, i, to)) break
            if (body.length > 0) break
            item.text += "\n" + line.replace(/^[ \t]+/, "")
            i++
        }

        while (body.length > 0 && isBlank(body[body.length - 1])) body.pop()
        if (body.length > 0) item.children = parseBlocks(body, 0, body.length, ctx)
        block.items.push(item)
    }
    return { block: block, next: i }
}

// ---------- tables ----------

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
    if (s.charAt(0) === "|") s = s.slice(1)
    if (s.length > 0 && s.charAt(s.length - 1) === "|" && s.charAt(s.length - 2) !== "\\") s = s.slice(0, -1)

    var cells = []
    var cur = ""
    for (var i = 0; i < s.length; i++) {
        var ch = s.charAt(i)
        var next = s.charAt(i + 1)
        if (ch === "\\" && (next === "|" || next === "\\")) { cur += next; i++; continue }
        if (ch === "|") { cells.push(cur.trim()); cur = ""; continue }
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
    if (t.indexOf("-") < 0 || !RE_PIPE.test(t)) return false
    var cells = tableCells(t)
    for (var i = 0; i < cells.length; i++) {
        if (!RE_DELIM_CELL.test(cells[i])) return false
    }
    return true
}

// ---------- prose runs ----------

// Every block used to be its own TextEdit, which meant a selection could never leave one paragraph.
// The daemon has no say in this: it is a rendering limit of "one document per block". Prose (paragraphs,
// headings, lists, quotes, tables) is grouped into a single run so one TextEdit holds all of it and Qt's
// own selection spans the whole thing. Code and tool cards stay separate — they are widgets, not text.
//
// Rendered as HTML rather than Markdown because MarkdownText takes Qt's fixed heading scale (2x / 1.5x
// / 1.1875x of the body), which is nothing like the Theme's 20 / 16 / 15, and it has no say over how a
// table is drawn. RichText lets every size, colour and margin come from Theme instead.
var PROSE = {
    p: true, h1: true, h2: true, h3: true, h4: true, h5: true, h6: true,
    ul: true, ol: true, quote: true, table: true, footnotes: true
}

function isProse(b) { return !!b && PROSE[b.type] === true }

// Consecutive prose blocks become one { type: "prose", items: [...] }.
function groupProse(blocks) {
    var out = []
    var run = null
    for (var i = 0; i < blocks.length; i++) {
        var b = blocks[i]
        if (isProse(b)) {
            if (!run) {
                run = { type: "prose", items: [], refs: b.refs }
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
// Order matters and is the whole implementation: escapes, code spans and tags the renderer supports
// are lifted out into placeholders first (so a later pass cannot eat them), then everything else is
// escaped, then links and emphasis are applied, then the placeholders are put back.
function inlineHtml(src, st, refs) {
    var held = []
    function hold(html) {
        held.push(html)
        return "\u0000" + (held.length - 1) + "\u0000"
    }
    function mono(text) {
        return '<span style="font-family:' + st.mono + ";background-color:" + st.codeBg
            + ";color:" + st.codeFg + ';">' + text + "</span>"
    }
    // Emphasis inside a link label: the label is lifted into a token before the emphasis pass runs,
    // so without this `[**bold**](url)` draws its asterisks.
    function emphasise(text) {
        var s = text
        s = s.replace(/\*\*\*([^\s*](?:[^*]*[^\s*])?)\*\*\*/g, "<b><i>$1</i></b>")
        s = s.replace(/\*\*([^\s*](?:[^*]*[^\s*])?)\*\*/g, "<b>$1</b>")
        s = s.replace(/__([^\s_](?:[^_]*[^\s_])?)__/g, "<b>$1</b>")
        // Emphasis follows CommonMark's flanking rule, loosely: the marker may not sit next to
        // whitespace on the inside. Without it `2 * 3 = 6` turns half the sentence italic, and prose
        // about pointers (`*char`, `**kwargs`) is exactly what a coding agent writes.
        s = s.replace(/(^|[^*\w])\*([^\s*](?:[^*]*[^\s*])?)\*(?=[^*\w]|$)/g, "$1<i>$2</i>")
        s = s.replace(/(^|[^_\w])_([^\s_](?:[^_]*[^\s_])?)_(?=[^_\w]|$)/g, "$1<i>$2</i>")
        s = s.replace(/~~([^\s~](?:[^~]*[^\s~])?)~~/g, "<s>$1</s>")
        return s
    }
    function link(label, url) {
        // Only the schemes the rest of the app is willing to open. Anything else keeps its label and
        // loses the link, rather than handing an unknown scheme to xdg-open.
        if (!/^(https?:\/\/|mailto:)/i.test(url)) return label
        return '<a href="' + url + '">' + label + "</a>"
    }

    var t = String(src === undefined || src === null ? "" : src)

    // 1. Backslash escapes: the author asked for the literal character, so it must not become markup.
    t = t.replace(/\\([\\`*_{}\[\]()#+\-.!|<>~=%])/g, function (_, ch) { return hold(escapeHtml(ch)) })

    // 2. Code spans, and the `==highlight==` extension, both before any emphasis pass.
    t = t.replace(/`([^`]+)`/g, function (_, code) { return hold(mono(escapeHtml(code))) })
    t = t.replace(/==([^=]+)==/g, function (_, code) { return hold('<span style="background-color:' + st.markBg + ';color:' + st.text + ';">' + escapeHtml(code) + "</span>") })

    // 3. Inline HTML that rich text can actually draw; the rest is left to be escaped as text.
    t = t.replace(RE_INLINE_TAG, function (whole, closing, name) {
        var n = name.toLowerCase()
        if (n === "br") return hold("<br/>")
        if (n === "code") return hold(closing ? "</span>" : mono(""))
        if (n === "kbd") {
            return hold(closing ? "</span>" : '<span style="font-family:' + st.mono + ";background-color:"
                + st.codeBg + ";color:" + st.codeFg + ';">')
        }
        if (n === "mark") {
            return hold(closing ? "</span>" : '<span style="background-color:' + st.markBg
                + ";color:" + st.text + ';">')
        }
        var map = { strong: "b", em: "i", del: "s", strike: "s" }
        var tag = map[n] || n
        return hold("<" + (closing ? "/" : "") + tag + ">")
    })
    // Autolinks before the unknown-tag strip below: `<https://…>` starts with a letter and would
    // otherwise be taken for a tag and deleted.
    t = t.replace(/<((?:https?:\/\/|mailto:)[^>\s]+)>/gi, function (_, url) {
        return hold(link(escapeHtml(url), url))
    })
    // Any other tag is HTML the renderer cannot honour; printing `<div>` in a reply helps nobody.
    t = t.replace(RE_HTML_TAG_ANY, "")

    t = escapeHtml(t)

    // 4. Footnotes before links: `[^1]` looks like a shortcut reference.
    var footnotes = refs && refs.__footnotes ? refs.__footnotes : null
    t = t.replace(/\[\^([^\]]+)\]/g, function (whole, label) {
        if (footnotes && !footnotes[label]) return whole
        return "<sup>" + hold('<span style="color:' + st.accent + ';">[' + label + "]</span>") + "</sup>"
    })

    // 5. Images that are inline — one inside a sentence, or inside a list item — stay as a link with
    // the alt text as its label. A standalone image never reaches this pass: parse() turns it into an
    // `image` block, which the transcript draws as a card (ImageBlock.qml). A picture drawn inside the
    // prose run would only be possible at its natural size, which overflows a narrow window.
    t = t.replace(/!\[([^\]]*)\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)/g, function (_, alt, url) {
        var label = alt !== "" ? alt : url
        return hold('<span style="color:' + st.quote + ';">[image: '
            + (label === url ? link(escapeHtml(url), url) : link(emphasise(escapeHtml(label)), url)) + "]</span>")
    })
    t = t.replace(/!\[([^\]]*)\]\[([^\]]*)\]/g, function (whole, alt, id) {
        var url = refs && refs[String(id || alt).toLowerCase()]
        if (!url) return whole
        return hold('<span style="color:' + st.quote + ';">[image: '
            + link(emphasise(escapeHtml(alt || url)), url) + "]</span>")
    })

    // 6. Links: inline, then reference, then shortcut, then autolink, then a bare URL.
    t = t.replace(/\[([^\]]*)\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)/g, function (_, label, url) {
        return hold(link(emphasise(label), url))
    })
    t = t.replace(/\[([^\]]*)\]\[([^\]]*)\]/g, function (whole, label, id) {
        var url = refs && refs[String(id || label).toLowerCase()]
        if (!url) return whole
        return hold(link(emphasise(label), url))
    })
    t = t.replace(/\[([^\]]+)\](?![\s]*[\(\[])/g, function (whole, label) {
        var url = refs && refs[String(label).toLowerCase()]
        if (!url) return whole
        return hold(link(emphasise(label), url))
    })
    // A bare URL, up to whitespace. Trailing punctuation is part of the sentence, not the address.
    t = t.replace(/(^|[^A-Za-z0-9])(https?:\/\/[^\s<>）]+)/g, function (_, lead, url) {
        var cut = url.replace(/[.,;:!?、。，）)\]]+$/, "")
        var rest = url.slice(cut.length)
        return lead + hold(link(escapeHtml(cut), cut)) + rest
    })

    // 7. Emphasis. `***x***` first, or the bold pass leaves the inner markers behind.
    t = emphasise(t)

    // Held HTML can itself hold a token (a code span inside a link label), so one pass would leave a
    // literal \u0000N\u0000 in the document. Each pass resolves a whole level, and a value can only
    // ever refer to a token created before it, so this terminates.
    for (var pass = 0; pass < 8 && t.indexOf("\u0000") >= 0; pass++)
        t = t.replace(/\u0000(\d+)\u0000/g, function (_, i) { return held[+i] })
    return t
}

// A block of text with its line breaks: two trailing spaces or a trailing backslash is a hard break,
// anything else is a soft one (CommonMark joins those with a space).
function inlineLines(src, st, refs) {
    var lines = String(src === undefined || src === null ? "" : src).split("\n")
    var out = ""
    for (var i = 0; i < lines.length; i++) {
        var line = lines[i]
        var hard = false
        if (i < lines.length - 1) {
            if (/ {2,}$/.test(line)) { hard = true; line = line.replace(/ +$/, "") }
            else if (/\\$/.test(line) && !/\\\\$/.test(line)) { hard = true; line = line.slice(0, -1) }
        }
        out += inlineHtml(line, st, refs)
        if (i < lines.length - 1) out += hard ? "<br/>" : " "
    }
    return out
}

// One block of a prose run as HTML.
//
// margin:0 everywhere, then a top margin on everything but the first: Qt gives <p> a ~12px default
// that would double up with the gap the caller already spaces blocks with.
function blockHtml(b, st, first, depth) {
    var t = b.type
    var refs = b.refs
    depth = depth || 0

    // Every branch builds the same base and only differs in type size, weight and colour.
    function style(extra) {
        return "margin:0;font-family:" + st.font + ";" + extra
    }
    function topGap() {
        return first ? 0 : st.gap
    }
    function body(text) {
        return inlineLines(text, st, refs)
    }

    if (t === "p" || t === "hr") {
        if (t === "hr") {
            // A rule nested in a quote or a list has no widget to draw it; a tinted sliver does.
            return "<p style=\"" + style("margin-top:" + topGap() + "px;background-color:" + st.tableLine
                + ";padding-top:1px;font-size:1px;") + "\">&nbsp;</p>"
        }
        return "<p style=\"" + style("margin-top:" + topGap() + "px;font-size:" + st.base + "px;color:" + st.text) + "\">"
            + body(b.text) + "</p>"
    }

    var heading = /^h([1-6])$/.exec(t)
    if (heading) {
        var level = parseInt(heading[1], 10)
        var size = st.sizes[level - 1] || st.base
        // Weight and colour, not only size: at the bottom of the scale two levels share a size, and a
        // heading the reader cannot place is worse than a smaller one.
        var color = level >= 5 ? st.quote : st.text
        return "<p style=\"" + style("margin-top:" + (first ? 0 : st.gap + st.headGap) + "px;font-size:"
            + size + "px;font-weight:600;color:" + color) + "\">" + body(b.text) + "</p>"
    }

    if (t === "quote")
        return quoteHtml(b, st, first, depth)

    // A fenced block nested in a quote or a list item. It cannot be a code card — a card is another
    // widget, and the run is one TextEdit — so it is monospace text in place. Highlighting is lost
    // with it, which is the trade for a quote that holds together.
    if (t === "code" || t === "tool") {
        return "<pre style=\"margin:0;" + (first ? "" : "margin-top:" + st.gap + "px;")
            + "font-family:" + st.mono + ";font-size:" + st.small + "px;background-color:" + st.codeBg
            + ";color:" + st.codeFg + ";padding:" + st.quotePad + "px;\">"
            + escapeHtml(String(b.code || "")) + "</pre>"
    }

    if (t === "ul" || t === "ol")
        return listHtml(b, st, first, depth)

    // A picture inside a quote or a list item: those blocks are drawn by this one TextEdit, so there is
    // no widget to hang an ImageBlock on. It becomes what an inline image has always been — the alt
    // text, linked — rather than an empty string, which would drop the line entirely.
    if (t === "image") {
        var imageUrl = String(b.url || "")
        var imageAlt = String(b.alt || "")
        var inner
        if (imageUrl !== "")
            inner = inlineHtml("![" + imageAlt + "](" + imageUrl + ")", st, refs)
        else
            inner = escapeHtml(imageAlt !== "" ? imageAlt : String(b.raw || ""))
        return "<p style=\"" + style("margin-top:" + topGap() + "px;font-size:" + st.base + "px;color:" + st.text) + "\">"
            + inner + "</p>"
    }

    if (t === "table")
        return tableHtml(b, st, first)

    if (t === "footnotes")
        return footnotesHtml(b, st, first)

    return ""
}

// A quote: a tinted block, indented by depth.
//
// Not a bar down the left side, which is what a Markdown renderer normally draws: Qt paints no border
// on a block element (measured — `border-left` on a div, a p and a blockquote all render nothing), and
// a table cell's background covers only its first line, so a cell cannot be a bar either. A background
// on the block itself does fill it, so the tint is what says "quoted".
function quoteHtml(b, st, first, depth) {
    var kids = b.children || []
    var inner = []
    for (var i = 0; i < kids.length; i++)
        inner.push(blockHtml(kids[i], st, i === 0, depth + 1))
    var bg = depth >= 1 ? st.quoteBgDeep : st.quoteBg
    return '<blockquote style="margin:0;' + (first ? "" : "margin-top:" + st.gap + "px;")
        + "background-color:" + bg + ";padding:" + st.quotePad + "px 0 " + st.quotePad + "px "
        + st.quoteIndent + "px;font-size:" + st.base + "px;color:" + st.quote + ';">'
        + inner.join("") + "</blockquote>"
}

function listHtml(b, st, first, depth) {
    var tag = b.type === "ul" ? "ul" : "ol"
    var start = (b.type === "ol" && b.start > 1) ? ' start="' + b.start + '"' : ""
    var items = b.items || []
    var lis = []
    for (var i = 0; i < items.length; i++) {
        var item = items[i]
        // A checkbox is a glyph, not a widget: rich text has no way to draw one that toggles, and a
        // task list the reader can see beats one that shows `[x]` as prose.
        var mark = item.checked === null || item.checked === undefined ? ""
            : (item.checked ? "&#9745; " : "&#9744; ")
        var inner = mark + inlineLines(item.text, st, b.refs)
        var kids = item.children || []
        for (var k = 0; k < kids.length; k++)
            inner += blockHtml(kids[k], st, false, depth + 1)
        lis.push("<li>" + inner + "</li>")
    }
    return "<" + tag + start + " style=\"margin:0;" + (first ? "" : "margin-top:" + st.gap + "px;")
        + "padding-left:" + st.listIndent + "px;font-size:" + st.base + "px;color:" + st.text + ';">'
        + lis.join("") + "</" + tag + ">"
}

function tableHtml(b, st, first) {
    var head = b.header || []
    var rows = b.rows || []
    var align = b.align || []
    var refs = b.refs

    function edge(i) { return align[i] || "left" }

    var cell = "font-family:" + st.font + ";font-size:" + st.base + "px;color:" + st.text + ";"
    var html = '<table cellspacing="0" cellpadding="' + st.cellPad + '" style="margin:0;'
        + (first ? "" : "margin-top:" + st.gap + "px;")
        + "border:1px solid " + st.tableLine + ";border-collapse:collapse;\">"

    var tr = "<tr>"
    for (var c = 0; c < head.length; c++)
        tr += "<th align=\"" + edge(c) + "\" style=\"" + cell + "font-weight:600;\">"
            + inlineHtml(head[c], st, refs) + "</th>"
    html += tr + "</tr>"

    for (var r = 0; r < rows.length; r++) {
        tr = "<tr>"
        var row = rows[r] || []
        for (var i = 0; i < head.length; i++) {
            var v = row[i] === undefined ? "" : row[i]
            tr += "<td align=\"" + edge(i) + "\" style=\"" + cell + "\">" + inlineHtml(v, st, refs) + "</td>"
        }
        html += tr + "</tr>"
    }
    return html + "</table>"
}

// Footnote definitions, at the end of the reply where they belong rather than inline where the model
// wrote them. The markers above them are not links: rich text can jump only to an anchor inside the
// same document, and the definitions are already below.
function footnotesHtml(b, st, first) {
    var items = b.items || []
    var out = []
    for (var i = 0; i < items.length; i++) {
        var it = items[i]
        out.push("<p style=\"margin:0;" + (i === 0 ? "" : "margin-top:" + st.quotePad + "px;")
            + "font-size:" + st.small + "px;color:" + st.quote + ";\">"
            + '<span style="color:' + st.accent + ';">[' + inlineHtml(it.label, st, null) + "]</span> "
            + inlineLines(it.text, st, b.refs) + "</p>")
    }
    return "<blockquote style=\"margin:0;" + (first ? "" : "margin-top:" + st.gap + "px;")
        + "background-color:" + st.quoteBg + ";padding:" + st.quotePad + "px 0 " + st.quotePad + "px "
        + st.quoteIndent + "px;\">" + out.join("") + "</blockquote>"
}

// A prose run (or a lone prose block) as RichText for one TextEdit.
function toHtml(blk, st) {
    if (!blk) return ""
    var items = blk.type === "prose" ? (blk.items || []) : [blk]
    var out = []
    for (var i = 0; i < items.length; i++)
        out.push(blockHtml(items[i], st, i === 0, 0))
    return out.join("")
}
