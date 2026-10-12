// Package herdr contains a generic client for the herdr socket API.
//
// Wire format (newline-delimited JSON over a Unix domain socket):
//
//	request:   {"id":"<id>","method":"<method>","params":{...}}
//	success:   {"id":"<id>","result":{...}}
//	error:     {"id":"<id>","error":{"code":"<code>","message":"<msg>"}}
//
// events.subscribe is acknowledged with {"id":"<id>","result":{"type":"subscription_started"}}
// and followed by pushed lines of the form {"event":"<kind>","data":{...}}.
// When a subscriber falls behind, the server sends an error line with the
// original request id and code "events_lost" and then closes the connection;
// that line is passed through like any other event.
package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	zeroTime = time.Time{}
	pastTime = time.Unix(1, 0)
)

// ErrClosed is returned when using a client or subscription that was closed.
var ErrClosed = errors.New("herdr: connection closed")

// Error is an error_response returned by the herdr server.
type Error struct{ Code, Message string }

func (e *Error) Error() string {
	return fmt.Sprintf("herdr: %s: %s", e.Code, e.Message)
}

type request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (r *response) err() error {
	if r.Error == nil {
		return nil
	}
	return &Error{Code: r.Error.Code, Message: r.Error.Message}
}

func encodeRequest(id, method string, params any) ([]byte, error) {
	raw := json.RawMessage(`{}`)
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("herdr: marshal params: %w", err)
		}
		raw = b
	}
	b, err := json.Marshal(request{ID: id, Method: method, Params: raw})
	if err != nil {
		return nil, fmt.Errorf("herdr: marshal request: %w", err)
	}
	return append(b, '\n'), nil
}

func dial(ctx context.Context, socketPath string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("herdr: dial %s: %w", socketPath, err)
	}
	return conn, nil
}

// readLine reads one full line (any length) without the trailing newline.
// Empty lines are skipped.
func readLine(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := r.ReadBytes('\n')
		for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
			line = line[:len(line)-1]
		}
		if len(line) > 0 {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// Client is a single herdr socket connection supporting concurrent calls.
type Client struct {
	conn net.Conn
	seq  atomic.Uint64

	wmu sync.Mutex // serialises writes

	mu      sync.Mutex
	pending map[string]chan *response

	done      chan struct{} // closed when the read loop ends
	err       error         // valid after done is closed
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool
}

// Dial connects to the herdr socket at socketPath.
func Dial(ctx context.Context, socketPath string) (*Client, error) {
	conn, err := dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:    conn,
		pending: make(map[string]chan *response),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	r := bufio.NewReader(c.conn)
	var loopErr error
	for {
		line, err := readLine(r)
		if err != nil {
			loopErr = err
			break
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil || resp.ID == "" {
			continue // not a response we can route
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		if ok {
			delete(c.pending, resp.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- &resp // buffered, never blocks
		}
	}
	if c.closed.Load() {
		loopErr = ErrClosed
	} else if errors.Is(loopErr, io.EOF) {
		loopErr = fmt.Errorf("%w by server", ErrClosed)
	} else {
		loopErr = fmt.Errorf("herdr: read: %w", loopErr)
	}
	c.err = loopErr
	close(c.done)
	_ = c.conn.Close()
}

func (c *Client) removePending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Call sends method with params and decodes the result into result (which may
// be nil). A server error_response is returned as *Error. It is safe for
// concurrent use.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return c.err
	default:
	}

	id := "c" + strconv.FormatUint(c.seq.Add(1), 10)
	buf, err := encodeRequest(id, method, params)
	if err != nil {
		return err
	}

	ch := make(chan *response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(ctx, buf); err != nil {
		c.removePending(id)
		return err
	}

	select {
	case resp := <-ch:
		return decodeResponse(resp, result)
	case <-c.done:
		select { // a response may have raced with shutdown
		case resp := <-ch:
			return decodeResponse(resp, result)
		default:
		}
		return c.err
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	}
}

func decodeResponse(resp *response, result any) error {
	if err := resp.err(); err != nil {
		return err
	}
	if result == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fmt.Errorf("herdr: decode result: %w", err)
	}
	return nil
}

func (c *Client) write(ctx context.Context, buf []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(dl)
	} else {
		_ = c.conn.SetWriteDeadline(zeroTime)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(pastTime) })
	_, err := c.conn.Write(buf)
	stop()
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		select {
		case <-c.done:
			return c.err
		default:
		}
		return fmt.Errorf("herdr: write: %w", err)
	}
	return nil
}

// Close closes the connection. It is idempotent.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.closeErr = c.conn.Close()
	})
	<-c.done
	return c.closeErr
}

// Subscription is a dedicated connection streaming events.
type Subscription struct {
	conn   net.Conn
	events chan json.RawMessage

	done      chan struct{} // closed by Close / ctx cancel to stop the reader
	closeOnce sync.Once
	closeErr  error
	closed    atomic.Bool

	mu   sync.Mutex
	err  error
	stop func() bool // releases the context.AfterFunc registration; guarded by mu
}

// Subscribe opens a dedicated connection, sends events.subscribe with params
// and waits for the acknowledgement. The subscription ends when ctx is
// cancelled, Close is called, or the server closes the connection.
func Subscribe(ctx context.Context, socketPath string, params any) (*Subscription, error) {
	conn, err := dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	buf, err := encodeRequest("sub1", "events.subscribe", params)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	s := &Subscription{
		conn:   conn,
		events: make(chan json.RawMessage, 64),
		done:   make(chan struct{}),
	}
	ack := make(chan error, 1)
	go s.readLoop(ack)

	stop := context.AfterFunc(ctx, func() { _ = s.shutdown() })
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
	}
	if _, err := conn.Write(buf); err != nil {
		stop()
		_ = s.shutdown()
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, fmt.Errorf("herdr: write: %w", err)
	}
	_ = conn.SetWriteDeadline(zeroTime)

	select {
	case err := <-ack:
		if err != nil {
			stop()
			_ = s.shutdown()
			return nil, err
		}
	case <-ctx.Done():
		stop()
		_ = s.shutdown()
		return nil, ctx.Err()
	}
	// From here the subscription lives until ctx is cancelled or Close;
	// shutdown releases the ctx registration so reconnects on a long-lived
	// context do not accumulate callbacks.
	s.setStop(stop)
	return s, nil
}

func (s *Subscription) readLoop(ack chan<- error) {
	defer close(s.events)
	defer func() { _ = s.shutdown() }() // closes the conn and releases the ctx registration even if the consumer never calls Close
	r := bufio.NewReader(s.conn)
	acked := false
	for {
		line, err := readLine(r)
		if err != nil {
			if s.closed.Load() {
				err = nil
			} else if errors.Is(err, io.EOF) {
				err = fmt.Errorf("%w by server", ErrClosed)
			} else {
				err = fmt.Errorf("herdr: read: %w", err)
			}
			if !acked {
				if err == nil {
					err = ErrClosed
				}
				ack <- err
			}
			s.setErr(err)
			return
		}
		if !acked {
			var resp response
			if err := json.Unmarshal(line, &resp); err != nil {
				ack <- fmt.Errorf("herdr: bad subscribe ack: %w", err)
				_ = s.shutdown()
				return
			}
			if e := resp.err(); e != nil {
				ack <- e
				_ = s.shutdown()
				return
			}
			acked = true
			ack <- nil
			continue
		}
		select {
		case s.events <- json.RawMessage(line):
		case <-s.done:
			return
		}
	}
}

func (s *Subscription) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

// setStop records the AfterFunc stop function. If the subscription already shut
// down (the callback fired first) it is released immediately.
func (s *Subscription) setStop(stop func() bool) {
	s.mu.Lock()
	s.stop = stop
	s.mu.Unlock()
	if s.closed.Load() {
		stop()
	}
}

func (s *Subscription) shutdown() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.done)
		s.closeErr = s.conn.Close()
		s.mu.Lock()
		stop := s.stop
		s.mu.Unlock()
		if stop != nil {
			stop()
		}
	})
	return s.closeErr
}

// Events returns the channel of raw event lines (one NDJSON line per element).
// It is closed when the connection ends, Close is called or the context
// passed to Subscribe is cancelled. Note that an events_lost error line is
// delivered like any other event, just before the server closes the stream.
func (s *Subscription) Events() <-chan json.RawMessage { return s.events }

// Err reports why the Events channel closed: nil after Close or context
// cancellation, non-nil (wrapping ErrClosed or a read error) otherwise. It is
// only meaningful once Events is closed.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the subscription. It is idempotent.
func (s *Subscription) Close() error {
	return s.shutdown()
}
