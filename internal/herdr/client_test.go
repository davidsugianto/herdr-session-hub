package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is an in-process NDJSON server on a temp Unix socket.
type fakeServer struct {
	t      *testing.T
	ln     net.Listener
	path   string
	handle func(conn net.Conn, r *bufio.Reader)
	wg     sync.WaitGroup
}

func newFakeServer(t *testing.T, handle func(conn net.Conn, r *bufio.Reader)) *fakeServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, ln: ln, path: path, handle: handle}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer func() { _ = conn.Close() }()
				handle(conn, bufio.NewReaderSize(conn, 1<<20))
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		s.wg.Wait()
	})
	return s
}

type reqMsg struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func readReq(t *testing.T, r *bufio.Reader) (reqMsg, bool) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return reqMsg{}, false
	}
	var m reqMsg
	if err := json.Unmarshal(line, &m); err != nil {
		t.Errorf("bad request %q: %v", line, err)
		return reqMsg{}, false
	}
	return m, true
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestClientCallSuccess(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		for {
			m, ok := readReq(t, r)
			if !ok {
				return
			}
			if m.Method != "ping" || string(m.Params) != `{"a":1}` {
				t.Errorf("unexpected request %+v", m)
			}
			_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","result":{"type":"pong","version":"x"}}` + "\n"))
		}
	})
	c, err := Dial(ctxT(t), srv.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	var res struct{ Type, Version string }
	if err := c.Call(ctxT(t), "ping", map[string]int{"a": 1}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Type != "pong" || res.Version != "x" {
		t.Fatalf("got %+v", res)
	}
}

func TestClientErrorResponse(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		m, ok := readReq(t, r)
		if !ok {
			return
		}
		_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","error":{"code":"not_found","message":"pane not found"}}` + "\n"))
	})
	c, err := Dial(ctxT(t), srv.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	err = c.Call(ctxT(t), "pane.get", map[string]string{"target": "w1:p9"}, nil)
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
	if he.Code != "not_found" || he.Message != "pane not found" {
		t.Fatalf("got %+v", he)
	}
}

func TestClientUnknownFieldsIgnored(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		m, ok := readReq(t, r)
		if !ok {
			return
		}
		// A stray line without our id, then a response with extra fields.
		_, _ = conn.Write([]byte(`{"id":"zzz","result":{}}` + "\n"))
		_, _ = conn.Write([]byte(`{"event":"x","data":{}}` + "\n"))
		_, _ = conn.Write([]byte(`{"extra":[1,2],"id":"` + m.ID + `","future":true,"result":{"n":7,"new":"field"}}` + "\n"))
	})
	c, err := Dial(ctxT(t), srv.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	var res struct{ N int }
	if err := c.Call(ctxT(t), "m", nil, &res); err != nil {
		t.Fatal(err)
	}
	if res.N != 7 {
		t.Fatalf("got %+v", res)
	}
}

func TestClientConcurrentOutOfOrder(t *testing.T) {
	const n = 20
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		var reqs []reqMsg
		for len(reqs) < n {
			m, ok := readReq(t, r)
			if !ok {
				return
			}
			reqs = append(reqs, m)
		}
		// Reply in reverse order.
		for i := len(reqs) - 1; i >= 0; i-- {
			m := reqs[i]
			_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","result":{"echo":` + string(m.Params) + `}}` + "\n"))
		}
	})
	c, err := Dial(ctxT(t), srv.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var res struct {
				Echo struct{ I int } `json:"echo"`
			}
			if err := c.Call(ctxT(t), "echo", map[string]int{"I": i}, &res); err != nil {
				t.Errorf("call %d: %v", i, err)
				return
			}
			if res.Echo.I != i {
				t.Errorf("call %d got reply for %d", i, res.Echo.I)
			}
		}(i)
	}
	wg.Wait()
}

func TestClientLargeLine(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		for {
			m, ok := readReq(t, r)
			if !ok {
				return
			}
			_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","result":{"text":"` + big + `"}}` + "\n"))
		}
	})
	c, err := Dial(ctxT(t), srv.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	var res struct{ Text string }
	if err := c.Call(ctxT(t), "pane.read", nil, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Text) != len(big) {
		t.Fatalf("got %d bytes, want %d", len(res.Text), len(big))
	}
}

func TestClientSubscribeEventsAndServerClose(t *testing.T) {
	big := strings.Repeat("y", 200<<10)
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		m, ok := readReq(t, r)
		if !ok {
			return
		}
		if m.Method != "events.subscribe" {
			t.Errorf("method %q", m.Method)
		}
		_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","result":{"type":"subscription_started"}}` + "\n"))
		_, _ = conn.Write([]byte(`{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1"}}` + "\n"))
		_, _ = conn.Write([]byte(`{"event":"pane.updated","data":{"big":"` + big + `"}}` + "\n"))
		_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","error":{"code":"events_lost","message":"lost"}}` + "\n"))
		// handler returns -> server closes conn
	})
	sub, err := Subscribe(ctxT(t), srv.path, map[string]any{
		"subscriptions": []map[string]string{{"type": "pane.agent_status_changed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()

	var got []string
	for ev := range sub.Events() {
		got = append(got, string(ev))
	}
	if len(got) != 3 {
		t.Fatalf("got %d events: %v", len(got), got)
	}
	if got[0] != `{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1"}}` {
		t.Fatalf("event 0: %s", got[0])
	}
	if len(got[1]) < len(big) {
		t.Fatalf("large event truncated: %d", len(got[1]))
	}
	if !strings.Contains(got[2], `"events_lost"`) {
		t.Fatalf("events_lost not passed through: %s", got[2])
	}
	if sub.Err() == nil {
		t.Fatal("Err should be set after server close")
	}
	if err := sub.Close(); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("close: %v", err)
	}
	_ = sub.Close() // idempotent
}

func TestClientSubscribeError(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		m, ok := readReq(t, r)
		if !ok {
			return
		}
		_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","error":{"code":"not_found","message":"no such pane"}}` + "\n"))
	})
	_, err := Subscribe(ctxT(t), srv.path, map[string]any{"subscriptions": []any{}})
	var he *Error
	if !errors.As(err, &he) || he.Code != "not_found" {
		t.Fatalf("got %v", err)
	}
}

func TestClientSubscribeCloseAndCtxCancel(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		m, ok := readReq(t, r)
		if !ok {
			return
		}
		_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","result":{"type":"subscription_started"}}` + "\n"))
		_, _ = r.ReadBytes('\n') // block until client disconnects
	})

	// Close
	sub, err := Subscribe(ctxT(t), srv.path, map[string]any{"subscriptions": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("events not closed after Close")
	}
	if sub.Err() != nil {
		t.Fatalf("Err after Close = %v", sub.Err())
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// ctx cancel
	ctx, cancel := context.WithCancel(context.Background())
	sub2, err := Subscribe(ctx, srv.path, map[string]any{"subscriptions": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, ok := <-sub2.Events():
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("events not closed after ctx cancel")
	}
	if sub2.Err() != nil {
		t.Fatalf("Err after cancel = %v", sub2.Err())
	}
}

func TestClientCtxCancel(t *testing.T) {
	received := make(chan struct{}, 1)
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		m, ok := readReq(t, r)
		if !ok {
			return
		}
		received <- struct{}{}
		// Never reply to the first request; answer later ones.
		for {
			m, ok = readReq(t, r)
			if !ok {
				return
			}
			_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","result":{}}` + "\n"))
		}
	})
	c, err := Dial(ctxT(t), srv.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- c.Call(ctx, "hang", nil, nil) }()
	<-received
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Call did not unblock on ctx cancel")
	}
	// Connection still usable afterwards.
	if err := c.Call(ctxT(t), "again", nil, nil); err != nil {
		t.Fatalf("call after cancel: %v", err)
	}
}

func TestClientCallAfterServerDeath(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		readReq(t, r) // read one request then drop the connection
	})
	c, err := Dial(ctxT(t), srv.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// In-flight call fails rather than hanging.
	if err := c.Call(ctxT(t), "x", nil, nil); err == nil {
		t.Fatal("expected error for in-flight call")
	}
	// Subsequent calls fail fast.
	done := make(chan error, 1)
	go func() { done <- c.Call(ctxT(t), "y", nil, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after death")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Call hung after server death")
	}
	if err := c.Close(); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("close: %v", err)
	}
	_ = c.Close() // idempotent
}

func TestClientCallAfterClose(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		_, _ = r.ReadBytes('\n')
	})
	c, err := Dial(ctxT(t), srv.path)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if err := c.Call(ctxT(t), "x", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
}

// afterFuncCtx counts live context.AfterFunc registrations. It relies on the
// context package's unexported afterFuncer hook (a ctx with an
// AfterFunc(func()) func() bool method is asked to register the callback), and
// Value is hidden so context.AfterFunc takes that path instead of treating this
// as a plain cancelCtx child. If a Go release changes that, this test fails
// loudly (the counter stays 0) rather than passing wrongly.
type afterFuncCtx struct {
	context.Context
	mu     sync.Mutex
	active int
}

func (c *afterFuncCtx) Value(any) any { return nil }

func (c *afterFuncCtx) AfterFunc(func()) func() bool {
	c.mu.Lock()
	c.active++
	c.mu.Unlock()
	var once sync.Once
	return func() bool {
		stopped := false
		once.Do(func() {
			c.mu.Lock()
			c.active--
			c.mu.Unlock()
			stopped = true
		})
		return stopped
	}
}

func (c *afterFuncCtx) activeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// A long-lived context must not accumulate a callback per Subscribe: the
// registration has to be released when the subscription is closed.
func TestClientSubscribeCloseReleasesContextCallback(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		m, ok := readReq(t, r)
		if !ok {
			return
		}
		_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","result":{"type":"subscription_started"}}` + "\n"))
		_, _ = r.ReadByte() // hold the connection open until the client closes it
	})
	ctx := &afterFuncCtx{Context: ctxT(t)}
	sub, err := Subscribe(ctx, srv.path, map[string]any{"subscriptions": []map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ctx.activeCount(); got != 1 {
		t.Fatalf("active callbacks while subscribed = %d, want 1", got)
	}
	_ = sub.Close()
	if got := ctx.activeCount(); got != 0 {
		t.Fatalf("active callbacks after Close = %d, want 0 (leak)", got)
	}
}

// If the server ends the subscription, the registration must be released even
// when the consumer never calls Close.
func TestClientSubscribeServerCloseReleasesContextCallback(t *testing.T) {
	srv := newFakeServer(t, func(conn net.Conn, r *bufio.Reader) {
		m, ok := readReq(t, r)
		if !ok {
			return
		}
		_, _ = conn.Write([]byte(`{"id":"` + m.ID + `","result":{"type":"subscription_started"}}` + "\n"))
		// handler returns -> server closes conn
	})
	ctx := &afterFuncCtx{Context: ctxT(t)}
	sub, err := Subscribe(ctx, srv.path, map[string]any{"subscriptions": []map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	for range sub.Events() { // drain until the channel closes
	}
	deadline := time.Now().Add(2 * time.Second)
	for ctx.activeCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ctx.activeCount(); got != 0 {
		t.Fatalf("active callbacks after server close = %d, want 0 (leak)", got)
	}
}
