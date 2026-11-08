package ipc

import (
	"encoding/json"
	"net"
	"sync"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
)

// Writer writes one JSON line to the connection and guarantees a single writer at a time.
// Stream events and responses come from different goroutines; without this lock the JSON would
// interleave.
type Writer struct {
	mu   sync.Mutex
	conn net.Conn
}

func NewWriter(conn net.Conn) *Writer {
	return &Writer{conn: conn}
}

func (w *Writer) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		applog.Warnf("ipc: encode failed: %v", err)
		return err
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.conn.Write(data)
	return err
}

// Respond returns a successful result.
func (w *Writer) Respond(id int, result any) {
	if err := w.writeJSON(Response{ID: id, Result: result}); err != nil {
		applog.Debugf("ipc: failed to write response: %v", err)
	}
}

// RespondError returns an error.
func (w *Writer) RespondError(id int, msg string) {
	applog.Debugf("ipc: method failed id=%d: %s", id, msg)
	if err := w.writeJSON(Response{ID: id, Error: msg}); err != nil {
		applog.Debugf("ipc: failed to write error response: %v", err)
	}
}

// WriteEvent pushes one event.
func (w *Writer) WriteEvent(ev Event) error {
	return w.writeJSON(ev)
}
