// Package highlight turns code into HTML with inline styles.
//
// HTML rather than a token array: QML's TextEdit understands Qt's rich-text subset, and inline
// color / font-weight / font-style are exactly what it supports. An intermediate format that
// QML then assembles into spans buys nothing.
//
// It lives in the daemon because QML has no parser and chroma is right here in Go.
package highlight

import (
	"bytes"
	"strconv"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// maxBytes caps a single block. Past this it is not code for a human to read, and forcing it
// through the highlighter only stalls the UI.
const maxBytes = 200 << 10

// One palette per background: github for light, github-dark for dark. Both keep comments
// readable without painting the whole block.
const (
	styleLight = "github"
	styleDark  = "github-dark"
)

const cacheLimit = 256

var (
	mu    sync.Mutex
	cache = make(map[string]string, cacheLimit)
	order = make([]string, 0, cacheLimit)
)

// HTML returns a <pre> block with an inline style on every token.
//
// An empty string means "not worth highlighting" (blank, too large, or no recognisable
// language), and the caller keeps plain text.
func HTML(lang, code string, light bool) (string, error) {
	if strings.TrimSpace(code) == "" || len(code) > maxBytes {
		return "", nil
	}

	key := cacheKey(lang, code, light)
	mu.Lock()
	if v, ok := cache[key]; ok {
		mu.Unlock()
		return v, nil
	}
	mu.Unlock()

	lexer := lexers.Get(strings.TrimSpace(lang))
	if lexer == nil && strings.TrimSpace(lang) == "" {
		// Models often omit the language; a guess beats nothing and is only a wrong colour.
		lexer = lexers.Analyse(code)
	}
	if lexer == nil {
		return "", nil
	}

	// Coalesce merges adjacent tokens of the same kind: well over half the spans disappear, and
	// Qt's HTML parsing cost is mostly span count.
	lexer = chroma.Coalesce(lexer)
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return "", err
	}

	style := styles.Get(styleDark)
	if light {
		style = styles.Get(styleLight)
	}
	if style == nil {
		style = styles.Fallback
	}

	// The body is formatted on its own and wrapped afterwards: a line that carries a background has
	// to be its own block element (see renderLines), so the <pre> cannot be written before the
	// lines are known.
	var buf bytes.Buffer
	formatter := html.New(html.WithClasses(false), html.TabWidth(4), html.PreventSurroundingPre(true))
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return "", err
	}

	// A style without a text colour leaves Colour unset, and String() then prints "#-00001" — an
	// invalid colour for an invalid reason. Unset means "inherit the card's", so say nothing.
	text := ""
	if c := style.Get(chroma.Text).Colour; c.IsSet() {
		text = c.String()
	}

	out := renderLines(buf.String(), text)
	mu.Lock()
	if len(order) >= cacheLimit {
		// Oldest out first. A real LRU is overkill: revisiting a block within one session is far
		// likelier than crossing 256 blocks, so FIFO does the job.
		delete(cache, order[0])
		order = order[1:]
	}
	cache[key] = out
	order = append(order, key)
	mu.Unlock()

	return out, nil
}

// renderLines turns chroma's body into one <pre> per group of lines, giving any line that carries a
// background colour a block element of its own.
//
// Qt's rich text cannot be trusted with a background inside <pre>: a span that ends where a line ends
// leaves its background painted over the *rest of the following line* (measured — a diff's red run
// coloured the tail of the green line below it). Closing the span before the newline does not help;
// the newline itself carries the paint. What does work is a block element per painted line, whose
// background fills the width of the block. So: a painted line becomes its own <pre> with the paint
// hoisted onto it, and every other line stays in one shared <pre>.
//
// The background is hoisted, not duplicated: a background left on the span inside would bring the
// bug back.
func renderLines(body, text string) string {
	lines := splitRuns(body)

	base := "margin:0;"
	if text != "" {
		base += "color:" + text + ";"
	}

	var b strings.Builder
	var group []string
	flush := func() {
		if len(group) == 0 {
			return
		}
		b.WriteString(`<pre style="` + base + `">`)
		b.WriteString(strings.Join(group, "\n"))
		b.WriteString("</pre>")
		group = group[:0]
	}

	// chroma's body ends with a newline, which splitRuns turns into a final empty line. A blank row
	// at the bottom of a card is height nobody asked for, and the last line keeps its own newline.
	for len(lines) > 0 && lines[len(lines)-1].html == "" && lines[len(lines)-1].paint == "" {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		if line.paint == "" {
			group = append(group, line.html)
			continue
		}
		flush()
		// The paint is the block's background, and its colour becomes the block's text colour so a
		// painted line stays legible even where a nested span carried no colour of its own.
		style := "margin:0;background-color:" + line.paint + ";"
		if line.color != "" {
			style += "color:" + line.color + ";"
		} else if text != "" {
			style += "color:" + text + ";"
		}
		b.WriteString(`<pre style="` + style + `">` + line.html + "</pre>")
	}
	flush()

	return b.String()
}

// one line of formatted code: its HTML with the background span removed, plus the paint that was
// taken off it.
type codeLine struct {
	html  string
	paint string
	color string
}

// splitRuns cuts the body into lines, tracking which spans are open so each line can be rendered on
// its own. A span carrying a background is reported once and dropped from the line's HTML.
func splitRuns(body string) []codeLine {
	var (
		out   []codeLine
		cur   strings.Builder
		stack []spanStyle
		paint string
		color string
	)

	reset := func() {
		paint, color = "", ""
	}

	closeAll := func() {
		for i := len(stack) - 1; i >= 0; i-- {
			// A hoisted span was never opened on this line, so closing it would unbalance the HTML.
			if stack[i].painted {
				continue
			}
			cur.WriteString("</span>")
		}
	}
	openAll := func() {
		for _, s := range stack {
			if s.painted {
				continue // hoisted to the block; leaving it here is what paints the wrong line
			}
			cur.WriteString(`<span style="` + s.style + `">`)
		}
	}

	for i := 0; i < len(body); {
		switch {
		case body[i] == '\n':
			closeAll()
			out = append(out, codeLine{html: cur.String(), paint: paint, color: color})
			cur.Reset()
			reset()
			openAll()
			i++

		case body[i] == '<':
			end := strings.IndexByte(body[i:], '>')
			if end < 0 {
				cur.WriteString(body[i:])
				i = len(body)
				continue
			}
			tag := body[i : i+end+1]
			switch {
			case strings.HasPrefix(tag, "<span") && !strings.HasSuffix(tag, "/>"):
				style := spanStyleOf(tag)
				painted := strings.Contains(style, "background-color")
				if painted && paint == "" {
					paint = styleValue(style, "background-color")
					color = styleValue(style, "color")
				}
				stack = append(stack, spanStyle{style: style, painted: painted})
				if !painted {
					cur.WriteString(tag)
				}
			case tag == "</span>" && len(stack) > 0:
				last := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if !last.painted {
					cur.WriteString(tag)
				}
			default:
				cur.WriteString(tag)
			}
			i += end + 1

		default:
			cur.WriteByte(body[i])
			i++
		}
	}

	closeAll()
	out = append(out, codeLine{html: cur.String(), paint: paint, color: color})
	for i := range out {
		out[i].html = dropEmptySpans(out[i].html)
	}
	return out
}

// dropEmptySpans removes <span …></span> pairs left by a token that held nothing — a diff hunk marker
// with no text of its own, for instance. Harmless to read, but they cost a tag pair each and Qt parses
// every one of them.
func dropEmptySpans(html string) string {
	for {
		trimmed := html
		for i := 0; i < len(trimmed); {
			if strings.HasPrefix(trimmed[i:], "<span") {
				gt := strings.IndexByte(trimmed[i:], '>')
				if gt > 0 {
					at := i + gt + 1
					if strings.HasPrefix(trimmed[at:], "</span>") {
						trimmed = trimmed[:i] + trimmed[at+len("</span>"):]
						continue
					}
				}
			}
			i++
		}
		if trimmed == html {
			return html
		}
		html = trimmed
	}
}

type spanStyle struct {
	style   string
	painted bool
}

// spanStyleOf is the attribute string of an opening <span style="…">.
func spanStyleOf(tag string) string {
	const marker = `style="`
	i := strings.Index(tag, marker)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(marker):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return rest
}

// styleValue reads one declaration out of a style attribute.
func styleValue(style, name string) string {
	for _, part := range strings.Split(style, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cacheKey(lang, code string, light bool) string {
	var b strings.Builder
	b.Grow(len(lang) + 24)
	b.WriteString(lang)
	b.WriteByte(0)
	if light {
		b.WriteByte('l')
	} else {
		b.WriteByte('d')
	}
	b.WriteByte(0)
	// Key is the length plus a slice from each end; collisions are far below "worth a sha256",
	// and hashing long code costs more than highlighting it.
	b.WriteString(strconv.Itoa(len(code)))
	b.WriteByte(0)
	if len(code) > 64 {
		b.WriteString(code[:32])
		b.WriteString(code[len(code)-32:])
	} else {
		b.WriteString(code)
	}
	return b.String()
}
