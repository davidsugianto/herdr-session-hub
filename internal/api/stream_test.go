package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/model"
)

type sseEvent struct {
	Event, ID, Data string
	Comment         bool
}

type sseClient struct {
	t      *testing.T
	resp   *http.Response
	events chan sseEvent
}

func (e *env) startServer() *httptest.Server {
	e.t.Helper()
	ts := httptest.NewServer(e.h)
	e.t.Cleanup(ts.Close)
	return ts
}

func connect(t *testing.T, ts *httptest.Server, path, lastID string) *sseClient {
	t.Helper()
	r, _ := http.NewRequest("GET", ts.URL+path, nil)
	r.Header.Set("Authorization", "Bearer tok-1")
	if lastID != "" {
		r.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	c := &sseClient{t: t, resp: resp, events: make(chan sseEvent, 4096)}
	t.Cleanup(c.close)
	go func() {
		defer close(c.events)
		sc := bufio.NewScanner(resp.Body)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.Event != "" || cur.Comment {
					c.events <- cur
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, ":"):
				cur.Comment = true
			case strings.HasPrefix(line, "event: "):
				cur.Event = line[7:]
			case strings.HasPrefix(line, "id: "):
				cur.ID = line[4:]
			case strings.HasPrefix(line, "data: "):
				cur.Data = line[6:]
			}
		}
	}()
	return c
}

func (c *sseClient) close() { _ = c.resp.Body.Close() }

func (c *sseClient) next() sseEvent {
	c.t.Helper()
	select {
	case ev, ok := <-c.events:
		if !ok {
			c.t.Fatal("stream closed")
		}
		return ev
	case <-time.After(3 * time.Second):
		c.t.Fatal("timed out waiting for SSE event")
	}
	return sseEvent{}
}

func (c *sseClient) expectNone(d time.Duration) {
	c.t.Helper()
	select {
	case ev := <-c.events:
		c.t.Fatalf("unexpected event %+v", ev)
	case <-time.After(d):
	}
}

func stackHas(sub string) bool {
	buf := make([]byte, 1<<20)
	return strings.Contains(string(buf[:runtime.Stack(buf, true)]), sub)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func change(n int) model.Change {
	return model.Change{Type: model.ChangeState, Session: "labs", Agent: agent("labs/term_1", "labs", model.StateBlocked, true),
		From: model.StateWorking, To: model.StateBlocked, At: time.Unix(int64(1000+n), 0).UTC()}
}

func TestStreamSnapshotThenChanges(t *testing.T) {
	e := newEnv(t)
	e.eng.sessions = []model.SessionInfo{{Name: "labs", Online: true, Agents: 2}}
	e.eng.agents["labs/u"] = agent("labs/u", "labs", model.StateUnknown, true)
	ts := e.startServer()
	c := connect(t, ts, "/api/stream", "")

	snap := c.next()
	if snap.Event != "snapshot" || snap.ID == "" {
		t.Fatalf("first event = %+v", snap)
	}
	var body struct {
		Sessions []model.SessionInfo `json:"sessions"`
		Agents   []model.Agent       `json:"agents"`
	}
	if err := json.Unmarshal([]byte(snap.Data), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || len(body.Agents) != 1 || body.Agents[0].ID != "labs/term_1" {
		t.Fatalf("snapshot = %+v", body)
	}

	e.eng.emit(change(1))
	e.eng.emit(change(2))
	a, b := c.next(), c.next()
	if a.Event != "change" || b.Event != "change" {
		t.Fatalf("events = %+v %+v", a, b)
	}
	ia, _ := strconv.ParseUint(a.ID, 10, 64)
	ib, _ := strconv.ParseUint(b.ID, 10, 64)
	is, _ := strconv.ParseUint(snap.ID, 10, 64)
	if is >= ia || ia >= ib {
		t.Fatalf("ids not increasing: snapshot=%d %d %d", is, ia, ib)
	}
	var ch model.Change
	if err := json.Unmarshal([]byte(a.Data), &ch); err != nil || ch.Type != model.ChangeState || ch.Agent.ID != "labs/term_1" {
		t.Fatalf("change = %+v (%v)", ch, err)
	}

	// include_unknown on the stream widens the snapshot.
	c2 := connect(t, ts, "/api/stream?include_unknown=1", "")
	if err := json.Unmarshal([]byte(c2.next().Data), &body); err != nil || len(body.Agents) != 2 {
		t.Fatalf("include_unknown snapshot = %+v (%v)", body, err)
	}
}

func TestStreamLastEventIDReplay(t *testing.T) {
	e := newEnv(t)
	ts := e.startServer()
	c := connect(t, ts, "/api/stream", "")
	c.next() // snapshot
	for i := 1; i <= 3; i++ {
		e.eng.emit(change(i))
	}
	ids := []string{c.next().ID, c.next().ID, c.next().ID}
	c.close()

	// Resume after the first change: only the 2nd and 3rd are replayed, no snapshot.
	r := connect(t, ts, "/api/stream", ids[0])
	for _, want := range ids[1:] {
		ev := r.next()
		if ev.Event != "change" || ev.ID != want {
			t.Fatalf("replay = %+v, want change %s", ev, want)
		}
	}
	r.expectNone(100 * time.Millisecond)

	// Live changes continue after the replay.
	e.eng.emit(change(4))
	if ev := r.next(); ev.Event != "change" {
		t.Fatalf("live = %+v", ev)
	}

	// Fully caught up: nothing replayed.
	r2 := connect(t, ts, "/api/stream", ids[2])
	ev := r2.next()
	if ev.Event != "change" { // the 4th change, newer than ids[2]
		t.Fatalf("caught-up resume = %+v", ev)
	}
	r2.expectNone(100 * time.Millisecond)
}

func TestStreamBadOrStaleLastEventIDGetsSnapshot(t *testing.T) {
	e := newEnv(t)
	ts := e.startServer()
	c := connect(t, ts, "/api/stream", "")
	c.next()
	e.eng.emit(change(1))
	last := c.next().ID
	lastN, _ := strconv.ParseUint(last, 10, 64)

	for _, id := range []string{"garbage", "-3", strconv.FormatUint(lastN+1000, 10), "1"} {
		r := connect(t, ts, "/api/stream", id)
		if ev := r.next(); ev.Event != "snapshot" {
			t.Errorf("Last-Event-ID %q → first event %+v, want snapshot", id, ev)
		}
		r.close()
	}
}

func TestStreamRingOverflowFallsBackToSnapshot(t *testing.T) {
	e := newEnv(t)
	ts := e.startServer()
	c := connect(t, ts, "/api/stream", "")
	c.next()
	const total = ringSize + 100
	for i := 1; i <= total; i++ {
		e.eng.emit(change(i))
	}
	var firstID, lastID string
	for i := 1; i <= total; i++ {
		ev := c.next()
		if ev.Event != "change" {
			t.Fatalf("event %d = %+v", i, ev)
		}
		if i == 1 {
			firstID = ev.ID
		}
		lastID = ev.ID
	}
	// Oldest ids have been evicted → snapshot, not a partial replay.
	if ev := connect(t, ts, "/api/stream", firstID).next(); ev.Event != "snapshot" {
		t.Fatalf("evicted id resume = %+v, want snapshot", ev)
	}
	// An id still inside the ring replays exactly the newer entries.
	lastN, _ := strconv.ParseUint(lastID, 10, 64)
	r := connect(t, ts, "/api/stream", strconv.FormatUint(lastN-ringSize+1, 10))
	for i := 0; i < ringSize-1; i++ {
		if ev := r.next(); ev.Event != "change" {
			t.Fatalf("replay %d = %+v", i, ev)
		}
	}
	r.expectNone(100 * time.Millisecond)
}

func TestRingSince(t *testing.T) {
	g := newRing(3, time.Time{}) // zero time → ids start at 1
	if _, ok := g.since(0); !ok {
		t.Fatal("empty ring, id 0 should be ok")
	}
	if _, ok := g.since(1); ok {
		t.Fatal("id beyond last must not be ok")
	}
	for i := 0; i < 5; i++ {
		g.add(change(i))
	} // ids 1..5, retained 3..5
	for _, c := range []struct {
		after uint64
		n     int
		ok    bool
	}{{0, 0, false}, {1, 0, false}, {2, 3, true}, {3, 2, true}, {5, 0, true}, {6, 0, false}} {
		out, ok := g.since(c.after)
		if ok != c.ok || len(out) != c.n {
			t.Errorf("since(%d) = %d,%v want %d,%v", c.after, len(out), ok, c.n, c.ok)
		}
	}
	if out, _ := g.since(2); out[0].id != 3 || out[2].id != 5 {
		t.Fatalf("ids = %d..%d", out[0].id, out[2].id)
	}
}

func TestStreamHeartbeat(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.HeartbeatInterval = 20 * time.Millisecond })
	ts := e.startServer()
	c := connect(t, ts, "/api/stream", "")
	c.next() // snapshot
	if ev := c.next(); !ev.Comment {
		t.Fatalf("expected heartbeat comment, got %+v", ev)
	}
}

func TestStreamDisconnectCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	eng, st := newFakeEngine(agent("labs/term_1", "labs", model.StateBlocked, true)), newFakeStore()
	h := New(ctx, Config{Engine: eng, Store: st, Actions: &fakeActions{}})
	ts := httptest.NewServer(h)
	defer ts.Close()

	var clients []*sseClient
	for i := 0; i < 5; i++ {
		c := connect(t, ts, "/api/stream", "")
		c.next()
		clients = append(clients, c)
	}
	if !stackHas("(*server).handleStream") {
		t.Fatal("expected live stream handlers")
	}
	for _, c := range clients {
		c.close()
	}
	waitFor(t, "stream handlers to exit after client disconnect", func() bool { return !stackHas("(*server).handleStream") })

	// Server shutdown ends open streams and the pump.
	c := connect(t, ts, "/api/stream", "")
	c.next()
	cancel()
	waitFor(t, "handler and pump to stop after ctx cancel", func() bool {
		return !stackHas("(*server).handleStream") && !stackHas("(*server).pump")
	})
	select {
	case _, ok := <-c.events:
		if ok {
			t.Log("event before close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream not closed after ctx cancel")
	}
}

// If the engine drops the API's subscription (slow consumer), the API must
// resubscribe and tell connected clients to resync instead of going stale.
func TestStreamSurvivesDroppedSubscription(t *testing.T) {
	e := newEnv(t)
	ts := e.startServer()
	c := connect(t, ts, "/api/stream", "")
	if ev := c.next(); ev.Event != "snapshot" {
		t.Fatalf("first event = %+v", ev)
	}
	waitFor(t, "initial subscription", func() bool { return e.eng.subCount() == 1 })

	e.eng.dropAll()
	if ev := c.next(); ev.Event != "snapshot" {
		t.Fatalf("after drop: event = %+v, want a fresh snapshot", ev)
	}
	waitFor(t, "resubscription", func() bool { return e.eng.subCount() == 1 })

	e.eng.emit(change(1))
	if ev := c.next(); ev.Event != "change" {
		t.Fatalf("after resubscribe: event = %+v, want change", ev)
	}
}
