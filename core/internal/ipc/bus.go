package ipc

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Ways a reverse request can fail. The caller (tools layer) only needs "no answer", but the
// UI side has to tell "the user refused" from "no UI attached", so the types must survive.
var (
	// ErrNoClient means this connection cannot answer reverse requests (a CLI, say).
	ErrNoClient = errors.New("this client does not support interactive requests")
	// ErrTimeout means it waited too long.
	ErrTimeout = errors.New("timed out waiting for the user to answer")
	// ErrDisconnected means the UI disconnected or closed the window before answering.
	ErrDisconnected = errors.New("connection lost")
)

// EventBus broadcasts from inside the daemon out to connections. Single process, so no
// cross-process pub/sub is involved.
type EventBus struct {
	mu   sync.RWMutex
	subs map[*Subscriber]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[*Subscriber]struct{})}
}

// Publish sends an event to every connection subscribed to that topic. With no subscribers it
// is dropped on purpose: events are never queued or replayed, and any state a client needs it
// asks for again.
func (b *EventBus) Publish(topic string, data any) {
	b.mu.RLock()
	subs := make([]*Subscriber, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.RUnlock()

	for _, s := range subs {
		s.deliver(Event{Topic: topic, Data: data})
	}
}

// HasSubscriber tells the daemon whether anyone is watching, so it can skip work nobody sees
// (no subscribers means no preview computation).
func (b *EventBus) HasSubscriber(topic string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.has(topic) {
			return true
		}
	}
	return false
}

func (b *EventBus) add(s *Subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[s] = struct{}{}
}

func (b *EventBus) remove(s *Subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, s)
}

// Subscriber is one connection's subscription state.
//
// Besides receiving events it carries server → client reverse requests (see Request): a
// permission dialog is only visible to one connection, so it cannot go through the EventBus and
// must be bound to that connection.
type Subscriber struct {
	bus    *EventBus
	writer *Writer

	mu     sync.RWMutex
	topics map[string]bool
	closed bool

	// Reverse request state. nextRID starts at 1; 0 means "not a reverse request".
	reqMu   sync.Mutex
	nextRID int
	replies map[int]chan reply

	closedCh chan struct{}
}

type reply struct {
	result any
	err    error
}

func (b *EventBus) NewSubscriber(w *Writer) *Subscriber {
	s := &Subscriber{
		bus:      b,
		writer:   w,
		topics:   make(map[string]bool),
		replies:  make(map[int]chan reply),
		closedCh: make(chan struct{}),
	}
	b.add(s)
	return s
}

func (s *Subscriber) Subscribe(topics []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range topics {
		if t != "" {
			s.topics[t] = true
		}
	}
}

func (s *Subscriber) Unsubscribe(topics []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range topics {
		delete(s.topics, t)
	}
}

// Topics returns the currently subscribed topics.
func (s *Subscriber) Topics() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.topics))
	for t := range s.topics {
		out = append(out, t)
	}
	return out
}

func (s *Subscriber) has(topic string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.topics[topic]
}

func (s *Subscriber) deliver(ev Event) {
	s.mu.RLock()
	ok := !s.closed && s.topics[ev.Topic]
	s.mu.RUnlock()
	if !ok {
		return
	}
	// A failed write only means the connection is gone; the read loop cleans up.
	_ = s.writer.WriteEvent(ev)
}

// Request sends a reverse request to this connection's client and waits for the answer.
//
// A state machine, not a callback: while waiting, the user may answer, the request may time
// out, the window may close (connection drops), or the run may be cancelled. Callers must treat
// any error as a refusal and must not assume only success and failure exist.
//
// The returned json.RawMessage is the client's result; interpreting it is up to the caller.
func (s *Subscriber) Request(ctx context.Context, method string, params any, timeout time.Duration) (any, error) {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return nil, ErrDisconnected
	}

	s.reqMu.Lock()
	if s.replies == nil {
		s.reqMu.Unlock()
		return nil, ErrDisconnected
	}
	s.nextRID++
	rid := s.nextRID
	ch := make(chan reply, 1)
	s.replies[rid] = ch
	s.reqMu.Unlock()

	defer func() {
		s.reqMu.Lock()
		delete(s.replies, rid)
		s.reqMu.Unlock()
	}()

	// Convert params to a map first, keeping ReverseRequest's own type simple.
	m, _ := params.(map[string]any)
	line := ReverseRequest{RID: rid, Method: method, Params: m, TimeoutMs: int(timeout / time.Millisecond)}
	if err := s.writer.writeJSON(line); err != nil {
		return nil, err
	}

	// timeout <= 0 means no limit: only ctx cancellation (the user pressing stop) or a dropped
	// connection ends it. Asking the user a question works this way — they may be busy elsewhere,
	// and being declared dead after five minutes would be wrong.
	var timerC <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timerC = timer.C
	}

	select {
	case r := <-ch:
		return r.result, r.err
	case <-timerC:
		return nil, ErrTimeout
	case <-s.closedCh:
		return nil, ErrDisconnected
	case <-ctx.Done():
		// On disconnect, ctx cancellation and closedCh fire almost together and Go picks one at
		// random. Picking ctx turns the message into a meaningless "context canceled" and hides that
		// the UI closed, so check the connection state first.
		select {
		case <-s.closedCh:
			return nil, ErrDisconnected
		case <-time.After(50 * time.Millisecond):
			return nil, ctx.Err()
		}
	}
}

// reply is called when the server reads the client's answer. An unknown rid (already timed
// out, already cleaned up) is dropped — that race is normal, not an error.
func (s *Subscriber) reply(r ReverseReply) {
	s.reqMu.Lock()
	ch := s.replies[r.RID]
	s.reqMu.Unlock()
	if ch == nil {
		return
	}
	var err error
	if r.Error != "" {
		err = classify(r.Error)
	}
	select {
	case ch <- reply{result: r.Result, err: err}:
	default:
	}
}

// classify restores sentinels from an error string that crossed a connection.
//
// Errors travel the wire as strings, so errors.Is cannot see through them; but the tools layer
// must tell "the user refused" from "no UI attached" — one can be explained to the model, the
// other has to tell the user to open the UI. The message text is part of the protocol; do not
// reword it.
func classify(msg string) error {
	switch msg {
	case ErrNoClient.Error():
		return ErrNoClient
	case ErrTimeout.Error():
		return ErrTimeout
	case ErrDisconnected.Error():
		return ErrDisconnected
	}
	return errors.New(msg)
}

func (s *Subscriber) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	// Wake every waiting reverse request at once, or the tool sits there until it times out.
	close(s.closedCh)
	s.bus.remove(s)
}
