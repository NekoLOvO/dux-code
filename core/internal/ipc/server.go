package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
)

// DefaultMaxLineSize is the per-line cap. Stream events are a kilobyte or two; 1 MiB leaves room
// for large replies like session.get.
const DefaultMaxLineSize = 1 << 20

// Handler handles one request and writes the response through Writer. sub is this connection's
// subscriber, usable when the handler wants to push events.
type Handler func(ctx context.Context, w *Writer, req Request, sub *Subscriber)

// Config describes a server.
type Config struct {
	AppName       string
	SocketPath    string
	APIVersion    int
	Version       string
	Capabilities  []string
	DefaultTopics []string
	MaxLineSize   int
	Bus           *EventBus
	OnSubscribe   func(topics []string, sub *Subscriber)
}

// Server is an NDJSON service on a Unix socket.
type Server struct {
	cfg      Config
	handler  Handler
	listener net.Listener
	socket   string
	bus      *EventBus

	mu      sync.Mutex
	stopped bool
}

func NewServer(cfg Config, handler Handler) *Server {
	if cfg.MaxLineSize <= 0 {
		cfg.MaxLineSize = DefaultMaxLineSize
	}
	bus := cfg.Bus
	if bus == nil {
		bus = NewEventBus()
	}
	return &Server{cfg: cfg, handler: handler, bus: bus}
}

func (s *Server) Bus() *EventBus     { return s.bus }
func (s *Server) SocketPath() string { return s.socket }
func (s *Server) Config() Config     { return s.cfg }

// Listen creates the socket. A stale one is removed — reaching this usually means the previous
// shutdown did not finish.
func (s *Server) Listen() error {
	path := s.cfg.SocketPath
	if path == "" {
		return errors.New("ipc: SocketPath is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("ipc: create socket dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("ipc: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("ipc: chmod socket: %w", err)
	}

	s.listener = ln
	s.socket = path
	applog.Debugf("ipc listening on %s", path)
	return nil
}

// Serve runs the accept loop until ctx ends or the listener closes.
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		return errors.New("ipc: Listen has not been called")
	}

	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return nil
			}
			return fmt.Errorf("ipc: accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil
	}
	s.stopped = true
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.socket != "" {
		_ = os.Remove(s.socket)
	}
	return nil
}

func (s *Server) handleConn(parent context.Context, conn net.Conn) {
	// A close error here belongs to a client that has already gone; every failure this connection can
	// report has been reported by the time the handler returns.
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	writer := NewWriter(conn)
	sub := s.bus.NewSubscriber(writer)
	defer sub.Close()

	caps := Capabilities{
		APIVersion:   s.cfg.APIVersion,
		Capabilities: s.cfg.Capabilities,
		Version:      s.cfg.Version,
		AppName:      s.cfg.AppName,
		PID:          os.Getpid(),
	}
	// First line: the handshake. The client is connected once it has read this.
	if err := writer.writeJSON(caps); err != nil {
		return
	}

	if len(s.cfg.DefaultTopics) > 0 {
		sub.Subscribe(s.cfg.DefaultTopics)
		if s.cfg.OnSubscribe != nil {
			s.cfg.OnSubscribe(s.cfg.DefaultTopics, sub)
		}
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), s.cfg.MaxLineSize)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			writer.RespondError(0, "invalid json: "+err.Error())
			continue
		}
		// A line with rid and no method answers a reverse request rather than starting one — hand it to
		// the waiting Subscriber instead of dispatching.
		if req.IsReply() {
			sub.reply(ReverseReply{RID: req.RID, Result: req.Result, Error: req.Error})
			continue
		}
		// One goroutine per request, so a streaming run cannot block a cancel. A panic in a handler must
		// not take the daemon down — the UI would just look like "all my settings vanished" — so recover
		// here.
		go func(r Request) {
			defer func() {
				if rec := recover(); rec != nil {
					applog.Errorf("handler panic on %s: %v\n%s", r.Method, rec, debug.Stack())
					writer.RespondError(r.ID, fmt.Sprintf("internal error: %v", rec))
				}
			}()
			s.dispatch(ctx, writer, r, sub)
		}(req)
	}
	if err := scanner.Err(); err != nil {
		applog.Debugf("ipc read error: %v", err)
	}

	// Read loop ended (client disconnected or was closed).
	cancel()
}

func (s *Server) dispatch(ctx context.Context, w *Writer, req Request, sub *Subscriber) {
	switch req.Method {
	case "ping":
		w.Respond(req.ID, map[string]any{"pong": true})
	case "subscribe":
		topics := toStrings(req.Get("topics"))
		sub.Subscribe(topics)
		if s.cfg.OnSubscribe != nil {
			s.cfg.OnSubscribe(topics, sub)
		}
		w.Respond(req.ID, map[string]any{"topics": sub.Topics()})
	case "unsubscribe":
		topics := toStrings(req.Get("topics"))
		sub.Unsubscribe(topics)
		w.Respond(req.ID, map[string]any{"topics": sub.Topics()})
	default:
		if s.handler == nil {
			w.RespondError(req.ID, "unknown method: "+req.Method)
			return
		}
		s.handler(ctx, w, req, sub)
	}
}

func toStrings(v any) []string {
	list, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
