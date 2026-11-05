package agent

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/llm"
	"github.com/NekoLOvO/dux-code/core/internal/store"
)

const (
	// maxImages is how many images one turn may send. Vision tokens are not cheap,
	// and most of the time the user only wants to paste one screenshot.
	maxImages = 4
	// maxImageBytes caps one image's raw size. Base64 adds a third on top,
	// still far from the 20MB ceilings, but a 4K screenshot costs a few thousand tokens on every turn.
	maxImageBytes = 8 << 20
)

// imageExts are the file extensions that look like images; they only decide whether to mention the
// file in the text.
// The real decision is sniffImage: extensions can lie, file headers cannot.
var imageExts = []string{".png", ".jpg", ".jpeg", ".gif", ".webp"}

func looksLikeImage(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range imageExts {
		if ext == e {
			return true
		}
	}
	return false
}

// sniffImage reports whether this MIME is a supported image; empty when not.
//
// Only four mainstream formats: anything else (svg, heic, tiff) is rejected upstream, and the error
// rarely says which image it was, leaving the user to try one at a time.
func sniffImage(b []byte) string {
	switch mime := http.DetectContentType(b); mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mime
	}
	return ""
}

// loadImages reads the images attached to this message.
//
// Unreadable, unsupported or oversized: log a warning and skip. The user would rather lose one image
// than have a strange file block the whole turn.
func loadImages(m *store.Message) []llm.Image {
	if m == nil || len(m.Attachments) == 0 {
		return nil
	}
	var out []llm.Image
	for _, p := range m.Attachments {
		if len(out) >= maxImages {
			applog.Warnf("more than %d attachments; the rest will not be sent to the model", maxImages)
			break
		}
		if !looksLikeImage(p) {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			applog.Warnf("cannot read image attachment %s: %v", p, err)
			continue
		}
		mime := sniffImage(raw)
		if mime == "" {
			applog.Warnf("attachment %s has an unsupported image header, skipping", p)
			continue
		}
		if len(raw) > maxImageBytes {
			applog.Warnf("image %s is too large (%d bytes > %d), skipping", p, len(raw), maxImageBytes)
			continue
		}
		out = append(out, llm.Image{MimeType: mime, Data: raw})
	}
	return out
}

// pendingImages takes the images the user attached this turn.
//
// Only the last user message: images from earlier messages are not resent, or every turn would pay
// for the same picture again. The cost is that the model forgets the image on the next turn — on
// purpose.
func (r *Runner) pendingImages(sessionID string) []llm.Image {
	hist := r.store.History(sessionID)
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role != "user" {
			continue
		}
		return loadImages(&hist[i])
	}
	return nil
}

// withImages attaches the images to the last user message (this turn's) and returns the same slice —
// modified in place, not copied. Callers always pass the fresh slice historyMessages just built, so
// there is no sharing problem.
func withImages(msgs []llm.Message, images []llm.Image) []llm.Message {
	if len(images) == 0 {
		return msgs
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			msgs[i].Images = images
			return msgs
		}
	}
	return msgs
}

// imageRejected detects "images attached, 4xx came back" — usually this model cannot see images.
//
// Dropping the images and retrying is not automatic: the model would answer a request without the
// picture while the user believes it saw one. Failing and saying why is better.
func imageRejected(err error, images []llm.Image) bool {
	if len(images) == 0 {
		return false
	}
	var he *llm.HTTPError
	return errors.As(err, &he) && he.Status >= 400 && he.Status < 500
}
