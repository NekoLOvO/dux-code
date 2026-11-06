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

// --- a background never lands inside a span ---
//
// Qt's rich text cannot hold a background inside <pre>: the paint leaks across the line break, so a
// diff's red run coloured the tail of the green line under it. The fix is structural — a painted line
// gets its own <pre> and the paint moves onto that block — so the invariant to guard is "no span
// carries a background, and every <pre> is balanced".
func TestBackgroundLivesOnBlocksNotSpans(t *testing.T) {
	cases := []struct {
		name string
		lang string
		code string
	}{
		{"diff", "diff", "- removed\n+ added\n  context\n"},
		{"diff with headers", "diff", "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"},
		{"no language", "", "a\nb\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := HTML(tc.lang, tc.code, false)
			if err != nil {
				t.Fatal(err)
			}
			if out == "" {
				t.Skip("not highlighted, so there is no background to place")
			}

			for _, span := range strings.Split(out, "<span")[1:] {
				head := span
				if end := strings.IndexByte(head, '>'); end >= 0 {
					head = head[:end]
				}
				if strings.Contains(head, "background-color") {
					t.Fatalf("a span carries a background, which Qt paints across a line break: %s", head)
				}
			}

			if got, want := strings.Count(out, "<pre"), strings.Count(out, "</pre>"); got != want {
				t.Errorf("%d <pre> opened, %d closed", got, want)
			}
			if got, want := strings.Count(out, "<span"), strings.Count(out, "</span>"); got != want {
				t.Errorf("%d <span> opened, %d closed:\n%s", got, want, out)
			}
			if strings.Contains(out, "<span></span>") || strings.Contains(out, "></span>") {
				t.Errorf("an empty span pair was left behind: %s", out)
			}
			if strings.Contains(out, "#-00001") {
				t.Errorf("an unset colour reached the output as an invalid hex: %s", out)
			}
		})
	}
}

// A painted line must carry the paint itself, or a diff card shows no colour at all.
func TestDiffLinesKeepTheirPaint(t *testing.T) {
	out, err := HTML("diff", "-old\n+new\n context\n", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"background-color:", "-old", "+new", "context"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from:\n%s", want, out)
		}
	}
	// The trailing newline chroma emits must not become an empty block at the bottom of the card.
	if strings.HasSuffix(out, `<pre style="margin:0;"></pre>`) {
		t.Errorf("an empty block trails the code: %s", out)
	}
}
