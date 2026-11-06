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
	"regexp"
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

	// Wrap <pre> here: chroma would add background-color and a pile of tab-size values, which Qt
	// either ignores or which would override the card's background. The card owns the
	// background; this carries text colour only and zeroes Qt's default pre margins.
	color := style.Get(chroma.Text).Colour.String()
	var buf bytes.Buffer
	if color != "" {
		buf.WriteString(`<pre style="color:` + color + `;margin:0;">`)
	} else {
		buf.WriteString(`<pre style="margin:0;">`)
	}

	formatter := html.New(html.WithClasses(false), html.TabWidth(4), html.PreventSurroundingPre(true))
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return "", err
	}
	buf.WriteString("</pre>")

	out := splitSpansAtLines(buf.String())
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

// splitSpansAtLines closes every open <span> at a newline and reopens it on the next line, but only
// where a span carries a background colour.
//
// Qt's rich text paints a span background lazily across the line break: a diff card's red or green
// run bleeds into the tail of the following line, which reads as the wrong line being marked. A
// foreground colour crossing a newline is harmless, so text-colour spans are left alone — otherwise a
// long block would grow a tag pair per line for nothing.
func splitSpansAtLines(html string) string {
	var b strings.Builder
	b.Grow(len(html) + len(html)/8)

	// open is the stack of span tags currently in effect; styled records which of them carry a
	// background, so the decision is "does any open span have one" rather than "is a span open".
	type span struct {
		tag   string
		paint bool
	}
	var open []span

	for i := 0; i < len(html); {
		if html[i] == '\n' {
			painted := false
			for _, s := range open {
				if s.paint {
					painted = true
					break
				}
			}
			if painted {
				// Close in reverse, then reopen in order: HTML nesting has to stay well formed.
				for j := len(open) - 1; j >= 0; j-- {
					b.WriteString("</span>")
				}
			}
			b.WriteByte('\n')
			if painted {
				for _, s := range open {
					b.WriteString(s.tag)
				}
			}
			i++
			continue
		}

		if html[i] == '<' {
			end := strings.IndexByte(html[i:], '>')
			if end < 0 {
				b.WriteString(html[i:])
				break
			}
			tag := html[i : i+end+1]
			switch {
			case strings.HasPrefix(tag, "<span") && !strings.HasSuffix(tag, "/>"):
				open = append(open, span{tag: tag, paint: strings.Contains(tag, "background-color")})
			case tag == "</span>" && len(open) > 0:
				open = open[:len(open)-1]
			}
			b.WriteString(tag)
			i += end + 1
			continue
		}

		b.WriteByte(html[i])
		i++
	}
	// A span that held nothing but the newline leaves an empty pair behind; with a background it
	// would paint a sliver at the start of the next line.
	out := b.String()
	for {
		trimmed := emptySpan.ReplaceAllString(out, "")
		if trimmed == out {
			return out
		}
		out = trimmed
	}
}

// An empty span pair. The tag bodies can be empty, so this has to be a regexp rather than a
// strings.Replace.
var emptySpan = regexp.MustCompile(`<span[^>]*></span>`)

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
