package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// sharedTransport shares one connection pool between Stream and FetchModels.
//
// Building a Transport / Client per call leaves keep-alive connections in a pool nobody reclaims —
// unnoticeable against a real API, but against a single-threaded local endpoint (some minimal HTTP
// server) it deadlocks every later request.
var sharedTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          16,
	MaxIdleConnsPerHost:   4,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 60 * time.Second,
}

// sharedClient has no overall timeout: a stream may run for minutes, and cancellation always goes
// through context. The silence watchdog in openStream is what bounds a stuck reply.
var sharedClient = &http.Client{Transport: sharedTransport}

// streamIdleTimeout is how long a reply may go completely silent before the connection is called
// dead.
//
// A provider that stops sending mid-answer leaves the read blocked: the socket is open, nobody is
// talking, and nothing in net/http times that out. http.Client.Timeout is not the answer — it caps
// the whole reply, so a healthy thirty-minute stream dies at the limit, which is exactly the wrong
// trade for a streaming client. The watchdog is fed by every byte that arrives (SSE keep-alive
// comments count), so a long answer is safe and only real silence trips it.
//
// Two minutes: reasoning models pause before their first token and a proxy flushing a large chunk
// can take seconds, but nothing legitimate goes quiet for that long. A var so tests can shorten it.
var streamIdleTimeout = 2 * time.Minute

// ErrStreamStalled reports a provider that stopped sending mid-reply: the connection was up and then
// simply went quiet.
//
// It is its own error rather than a generic read failure because the two are treated differently: a
// stall is worth retrying (nothing arrived, nothing was said), and the user deserves to know the
// reply was cut by silence rather than by them pressing stop.
var ErrStreamStalled = errors.New("the reply stopped arriving")

// streamGuard is a streaming response body with a silence watchdog attached.
type streamGuard struct {
	body    io.ReadCloser
	timer   *time.Timer
	cancel  context.CancelFunc
	stalled atomic.Bool
}

// Read feeds the watchdog. Any byte — including an SSE keep-alive — proves the other end is alive.
func (g *streamGuard) Read(p []byte) (int, error) {
	n, err := g.body.Read(p)
	if n > 0 {
		g.timer.Reset(streamIdleTimeout)
	}
	return n, err
}

// Close stops the watchdog and releases the connection.
func (g *streamGuard) Close() error {
	g.timer.Stop()
	g.cancel()
	return g.body.Close()
}

// openStream sends a streaming request with the watchdog armed.
//
// The cancellation is what makes this work: net/http watches the request's context and closes the
// connection when it is done, so a read blocked in the transport returns instead of waiting for a
// packet that will never come.
func openStream(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, *streamGuard, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	req = req.WithContext(streamCtx)

	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}

	g := &streamGuard{body: resp.Body, cancel: cancel}
	g.timer = time.AfterFunc(streamIdleTimeout, func() {
		g.stalled.Store(true)
		cancel()
	})
	return resp, g, nil
}

// streamErr says what actually broke a read: the user pressed stop, the provider went silent, or the
// connection failed some other way.
//
// Without this, a stall surfaces as "context canceled" and reads exactly like the user's own stop.
func streamErr(ctx context.Context, g *streamGuard, err error) error {
	// The user's own cancellation wins: it is the more useful explanation even if the watchdog fired
	// in the same instant.
	if ctx.Err() != nil {
		return ErrCancelled
	}
	if g != nil && g.stalled.Load() {
		return fmt.Errorf("%w (nothing for %s, so the connection was dropped)", ErrStreamStalled, streamIdleTimeout)
	}
	return err
}
