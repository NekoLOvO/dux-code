package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Client is the client the CLI uses.
//
// Reading runs on its own goroutine: responses and events arrive on the same connection, so
// reading only while waiting for a response would leave stream events piling up in the socket
// buffer.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	caps Capabilities

	writeMu sync.Mutex
	idMu    sync.Mutex
	nextID  int

	pendMu  sync.Mutex
	pending map[int]chan callResult

	evMu      sync.RWMutex
	onEvent   func(Event)
	onRequest func(method string, params map[string]any) (any, error)

	closeOnce sync.Once
	closed    chan struct{}
}

type callResult struct {
	result json.RawMessage
	err    error
}

// Dial connects to the socket, consumes the handshake line, then starts the read loop.
func Dial(socketPath string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", socketPath, err)
	}

	r := bufio.NewReaderSize(conn, 64*1024)
	line, err := r.ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read handshake: %w", err)
	}
	var caps Capabilities
	if err := json.Unmarshal(line, &caps); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("parse handshake: %w", err)
	}

	c := &Client{
		conn:    conn,
		r:       r,
		caps:    caps,
		pending: make(map[int]chan callResult),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) Capabilities() Capabilities { return c.caps }

// Close closes the connection. It is idempotent, so the second call reports nothing — the first one
// already did, and returning a stale error twice is how a deferred close ends up hiding the real
// failure of the function it is deferring in.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.conn.Close()
	})
	return err
}

// OnEvent sets the event callback; it can be replaced at any time.
func (c *Client) OnEvent(fn func(Event)) {
	c.evMu.Lock()
	defer c.evMu.Unlock()
	c.onEvent = fn
}

// OnRequest sets the handler for reverse requests (when the daemon asks the client something).
// With no handler everything is answered "unsupported", so the tools layer treats it as a
// refusal instead of waiting out a timeout. The returned value goes into
// {"rid":N,"result":...}.
func (c *Client) OnRequest(fn func(method string, params map[string]any) (any, error)) {
	c.evMu.Lock()
	defer c.evMu.Unlock()
	c.onRequest = fn
}

// writeLine is the only write point; stream events and responses share one lock.
func (c *Client) writeLine(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.conn.Write(append(data, '\n'))
	return err
}

// Call sends a request and waits for its response (events keep being dispatched by the read
// loop).
func (c *Client) Call(method string, params map[string]any) (json.RawMessage, error) {
	c.idMu.Lock()
	c.nextID++
	id := c.nextID
	c.idMu.Unlock()

	ch := make(chan callResult, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()

	defer func() {
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
	}()

	if err := c.writeLine(Request{ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}

	select {
	case res := <-ch:
		return res.result, res.err
	case <-c.closed:
		return nil, fmt.Errorf("connection is closed")
	}
}

// answerReverse answers a reverse request. Without a handler it answers "unsupported" so the
// daemon can refuse the tool at once instead of waiting 60 seconds.
func (c *Client) answerReverse(rid int, method string, params map[string]any) {
	c.evMu.RLock()
	fn := c.onRequest
	c.evMu.RUnlock()

	out := ReverseReply{RID: rid}
	if fn == nil {
		out.Error = ErrNoClient.Error()
	} else {
		res, err := fn(method, params)
		if err != nil {
			out.Error = err.Error()
		} else {
			out.Result = res
		}
	}
	_ = c.writeLine(out)
}

type envelope struct {
	ID     *int            `json:"id"`
	RID    *int            `json:"rid"`
	Method string          `json:"method"`
	Params map[string]any  `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Event  string          `json:"event"`
	Data   json.RawMessage `json:"data"`
}

func (c *Client) readLoop() {
	// The loop ends because the connection broke or the client closed it; either way there is nobody
	// left who could act on a close error, and the caller is told through failAll.
	defer func() { _ = c.Close() }()

	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			c.failAll(fmt.Errorf("connection lost: %w", err))
			return
		}

		var env envelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue // a bad line must not kill the connection
		}

		// A reverse request (daemon → client). It must be answered or the other side waits until it
		// times out. It has to run on its own goroutine: the handler may do something slow (show a
		// dialog, wait for a click), which would block this connection's reads and stop events too.
		if env.RID != nil && env.Method != "" {
			go c.answerReverse(*env.RID, env.Method, env.Params)
			continue
		}

		// event
		if env.Event != "" {
			c.evMu.RLock()
			fn := c.onEvent
			c.evMu.RUnlock()
			if fn != nil {
				fn(Event{Topic: env.Event, Data: env.Data})
			}
			continue
		}

		// response
		if env.ID == nil {
			continue
		}
		c.pendMu.Lock()
		ch, ok := c.pending[*env.ID]
		c.pendMu.Unlock()
		if !ok {
			continue
		}
		if env.Error != "" {
			ch <- callResult{err: fmt.Errorf("%s", env.Error)}
		} else {
			ch <- callResult{result: env.Result}
		}
	}
}

func (c *Client) failAll(err error) {
	c.pendMu.Lock()
	defer c.pendMu.Unlock()
	for _, ch := range c.pending {
		select {
		case ch <- callResult{err: err}:
		default:
		}
	}
}
