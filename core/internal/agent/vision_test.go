package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/store"
)

// A real PNG header is enough for sniffing; the content does not matter.
const pngHeader = "\x89PNG\r\n\x1a\n"

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The header decides, not the extension: renaming a file must not get it sent as an image.
func TestLoadImagesSniffsHeaderNotExtension(t *testing.T) {
	dir := t.TempDir()
	png := writeFile(t, dir, "shot.png", pngHeader+"fake")
	txt := writeFile(t, dir, "notes.txt", "hello")
	fakePNG := writeFile(t, dir, "lies.png", "not really a png")
	missing := filepath.Join(dir, "gone.png")

	got := loadImages(&store.Message{Attachments: []string{png, txt, fakePNG, missing}})
	if len(got) != 1 {
		t.Fatalf("only the real PNG should survive, got %d", len(got))
	}
	if got[0].MimeType != "image/png" {
		t.Errorf("wrong MIME: %q", got[0].MimeType)
	}
}

// Extra images are dropped: vision tokens are expensive, and pasting 20 should not burn a turn.
func TestLoadImagesCapsCount(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < maxImages+2; i++ {
		paths = append(paths, writeFile(t, dir, "p"+string(rune('a'+i))+".png", pngHeader))
	}
	if got := loadImages(&store.Message{Attachments: paths}); len(got) != maxImages {
		t.Errorf("should keep %d images, got %d", maxImages, len(got))
	}
}

// Images attach to this turn's user message.
func TestWithImagesAttachesToLastUserMessage(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "old"},
		{Role: llm.RoleAssistant, Content: "ok"},
		{Role: llm.RoleTool, Content: "result"},
		{Role: llm.RoleUser, Content: "new"},
	}
	got := withImages(msgs, []llm.Image{{MimeType: "image/png"}})
	if len(got[0].Images) != 0 {
		t.Error("older messages must not get the images")
	}
	if len(got[3].Images) != 1 {
		t.Error("images belong on the last user message")
	}

	// Without images nothing changes (use a fresh copy; the one above was modified in place).
	fresh := []llm.Message{{Role: llm.RoleUser, Content: "new"}}
	if out := withImages(fresh, nil); len(out[0].Images) != 0 {
		t.Error("without images nothing should change")
	}
}

// Images are not listed in the text: listed, the model tries read_file on the binary.
func TestMessageContentSkipsImagePaths(t *testing.T) {
	got := messageContent(store.Message{
		Text:        "look at this",
		Attachments: []string{"/tmp/shot.png", "/tmp/main.go"},
	})
	if strings.Contains(got, "shot.png") {
		t.Errorf("image paths must not appear in the text: %q", got)
	}
	if !strings.Contains(got, "main.go") {
		t.Errorf("non-image attachments must still be listed: %q", got)
	}

	// With images only, do not leave an empty "attached files" line behind.
	only := messageContent(store.Message{Text: "look", Attachments: []string{"/tmp/shot.png"}})
	if only != "look" {
		t.Errorf("with only an image the text should remain alone, got %q", only)
	}
}
