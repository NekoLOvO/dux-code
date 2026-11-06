package highlight

import (
	"strings"
	"testing"
)

const sample = "func main() {\n\t// greet\n\tfmt.Println(\"hi\", 1+2)\n}\n"

func TestGoGetsInlineStyles(t *testing.T) {
	out, err := HTML("go", sample, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("HTML: %q", out)

	if !strings.HasPrefix(out, "<pre") {
		t.Errorf("should be a <pre> block: %q", out[:min(20, len(out))])
	}
	if !strings.Contains(out, "style=\"color:") {
		t.Error("no inline style means no colour on the QML side")
	}
	if !strings.Contains(out, "func") || !strings.Contains(out, "main") {
		t.Error("the code content disappeared")
	}
	// Newlines must survive: Qt only keeps whitespace inside <pre>.
	if !strings.Contains(out, "\n") {
		t.Error("the newline was eaten")
	}
}

// A block without a language must be guessed; an unknown one returns "" (caller keeps plain text).
func TestLanguageGuessing(t *testing.T) {
	out, err := HTML("", sample, true)
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Error("Go code should be detected")
	}
	if again, _ := HTML("nonsense-lang", sample, true); again != "" {
		t.Log("unknown language falls back to nil detection:", again[:min(30, len(again))])
	}
}

func TestEmptyAndOversize(t *testing.T) {
	if out, _ := HTML("go", "   \n\t", false); out != "" {
		t.Error("blank input should not be highlighted")
	}
	big := strings.Repeat("a", maxBytes+1)
	if out, _ := HTML("go", big, false); out != "" {
		t.Error("input above the limit should not be highlighted")
	}
}

func TestCacheReturnsIdentical(t *testing.T) {
	first, _ := HTML("go", sample, false)
	second, _ := HTML("go", sample, false)
	if first != second || first == "" {
		t.Error("the same code should produce the same result")
	}
	// Light and dark differ and must not share a cache entry.
	dark, _ := HTML("go", sample, true)
	if dark == first {
		t.Error("light and dark styles must differ")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- no span crosses a newline ---
//
// Qt paints a span's background lazily across a line break, so a diff card's red run bled into the
// tail of the next line and marked the wrong one. A painted span must therefore end where its line
// ends.
func TestBackgroundSpansStopAtNewline(t *testing.T) {
	cases := []struct {
		name string
		lang string
		code string
	}{
		{"diff", "diff", "- removed\n+ added\n  context\n"},
		{"two hunks", "diff", "@@ -1 +1 @@\n-old\n+new\n"},
		{"no language", "", "a\nb\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := HTML(tc.lang, tc.code, false)
			if err != nil {
				t.Fatal(err)
			}
			if out == "" {
				t.Skip("not highlighted, so there is no span to test")
			}

			// Walk the HTML and fail as soon as a painted span is still open at a newline.
			var painted int
			for i := 0; i < len(out); i++ {
				switch {
				case out[i] == '\n':
					if painted > 0 {
						t.Fatalf("a background span is still open at a newline:\n%s", out)
					}
				case strings.HasPrefix(out[i:], "<span"):
					end := strings.IndexByte(out[i:], '>')
					if end < 0 {
						break
					}
					if strings.Contains(out[i:i+end], "background-color") {
						painted++
					}
					i += end
				case strings.HasPrefix(out[i:], "</span>"):
					if painted > 0 {
						painted--
					}
					i += len("</span>") - 1
				}
			}
			if strings.Contains(out, "></span>") {
				t.Errorf("an empty span pair was left behind: %s", out)
			}
		})
	}
}
