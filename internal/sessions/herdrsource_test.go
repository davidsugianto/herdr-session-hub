package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/herdr"
)

// fakeHerdr is an in-process herdr server: it answers session.snapshot and
// agent.get from fixtures and, for events.subscribe, acknowledges and then
// pushes whatever the test sends on push, closing the connection when push is
// closed.
type fakeHerdr struct {
	t    *testing.T
	path string
	ln   net.Listener
	wg   sync.WaitGroup

	mu       sync.Mutex
	snapshot herdr.SessionSnapshot
	agents   map[string]herdr.AgentInfo // by pane id, for agent.get
	subReqs  []herdr.EventsSubscribeParams
	subErrs  []string            // if non-empty, the next events.subscribe fails with this error code
	snapSeq  [][]herdr.AgentInfo // successive session.snapshot answers; the last one repeats
	getErr   string              // if set, agent.get fails with this error code
	push     chan string
}

func newFakeHerdr(t *testing.T) *fakeHerdr {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeHerdr{t: t, path: filepath.Join(dir, "h.sock"), agents: map[string]herdr.AgentInfo{}, push: make(chan string, 16)}
	f.ln, err = net.Listen("unix", f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := f.ln.Accept()
			if err != nil {
				return
			}
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				defer func() { _ = conn.Close() }()
				f.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = f.ln.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeHerdr) serve(conn net.Conn) {
	r := bufio.NewReaderSize(conn, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		write := func(s string) { _, _ = conn.Write([]byte(s + "\n")) }
		switch req.Method {
		case herdr.MethodSessionSnapshot:
			f.mu.Lock()
			snap := f.snapshot
			if len(f.snapSeq) > 0 {
				snap = herdr.SessionSnapshot{Agents: f.snapSeq[0]}
				if len(f.snapSeq) > 1 {
					f.snapSeq = f.snapSeq[1:]
				}
			}
			b, _ := json.Marshal(herdr.SessionSnapshotResult{Type: herdr.ResultSessionSnapshot, Snapshot: snap})
			f.mu.Unlock()
			write(`{"id":"` + req.ID + `","result":` + string(b) + `}`)
		case herdr.MethodAgentGet:
			var p herdr.AgentGetParams
			_ = json.Unmarshal(req.Params, &p)
			f.mu.Lock()
			a, ok := f.agents[p.Target]
			getErr := f.getErr
			f.mu.Unlock()
			if getErr != "" {
				write(`{"id":"` + req.ID + `","error":{"code":"` + getErr + `","message":"boom"}}`)
				continue
			}
			if !ok {
				// the code the real herdr 0.9.3 uses for an unknown agent target
				write(`{"id":"` + req.ID + `","error":{"code":"agent_not_found","message":"no such agent"}}`)
				continue
			}
			b, _ := json.Marshal(herdr.AgentGetResult{Type: herdr.ResultAgentInfo, Agent: a})
			write(`{"id":"` + req.ID + `","result":` + string(b) + `}`)
		case herdr.MethodEventsSubscribe:
			var p herdr.EventsSubscribeParams
			_ = json.Unmarshal(req.Params, &p)
			f.mu.Lock()
			f.subReqs = append(f.subReqs, p)
			var failCode string
			if len(f.subErrs) > 0 {
				failCode, f.subErrs = f.subErrs[0], f.subErrs[1:]
			}
			f.mu.Unlock()
			if failCode != "" {
				write(`{"id":"` + req.ID + `","error":{"code":"` + failCode + `","message":"rejected"}}`)
				return
			}
			write(`{"id":"` + req.ID + `","result":{"type":"subscription_started"}}`)
			gone := make(chan struct{})
			go func() { _, _ = r.ReadByte(); close(gone) }() // client hung up
			for {
				select {
				case l, ok := <-f.push:
					if !ok {
						return
					}
					write(l)
				case <-gone:
					return
				}
			}
		}
	}
}

func (f *fakeHerdr) setAgents(infos ...herdr.AgentInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot = herdr.SessionSnapshot{Agents: infos}
	for _, a := range infos {
		f.agents[a.PaneID] = a
	}
}

func (f *fakeHerdr) subscribeRequests() []herdr.EventsSubscribeParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]herdr.EventsSubscribeParams(nil), f.subReqs...)
}

func info(pane, term string, st herdr.AgentStatus) herdr.AgentInfo {
	return herdr.AgentInfo{PaneID: pane, TerminalID: term, AgentStatus: st, Agent: "claude", Name: "n-" + term, Cwd: "/work/" + term, TerminalTitleStripped: "t-" + term}
}

func nextEvent(t *testing.T, st Stream) SourceEvent {
	t.Helper()
	select {
	case ev, ok := <-st.Events():
		if !ok {
			t.Fatal("stream closed, wanted an event")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return SourceEvent{}
}

func TestHerdrSourceSnapshotMapsAgents(t *testing.T) {
	f := newFakeHerdr(t)
	f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusWorking), info("w1:p2", "term_b", herdr.AgentStatusBlocked))
	got, err := NewHerdrSource(f.path).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{
		{ID: "term_a", PaneID: "w1:p1", Name: "n-term_a", Kind: "claude", Status: "working", Cwd: "/work/term_a", Title: "t-term_a"},
		{ID: "term_b", PaneID: "w1:p2", Name: "n-term_b", Kind: "claude", Status: "blocked", Cwd: "/work/term_b", Title: "t-term_b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agents = %+v\nwant     %+v", got, want)
	}
}

// herdr has no session-wide status subscription: pane.agent_status_changed
// needs a pane_id, so Subscribe must ask for one entry per current agent pane
// plus the lifecycle events that signal topology changes.
func TestHerdrSourceSubscribeAsksPerPaneStatus(t *testing.T) {
	f := newFakeHerdr(t)
	f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusWorking), info("w2:p1", "term_b", herdr.AgentStatusIdle))
	st, err := NewHerdrSource(f.path).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	reqs := f.subscribeRequests()
	if len(reqs) != 1 {
		t.Fatalf("subscribe requests = %d, want 1", len(reqs))
	}
	var lifecycle, status []string
	for _, s := range reqs[0].Subscriptions {
		if s.Type == herdr.SubPaneAgentStatusChanged {
			status = append(status, s.PaneID)
		} else {
			lifecycle = append(lifecycle, s.Type)
		}
	}
	sort.Strings(status)
	sort.Strings(lifecycle)
	if want := []string{"w1:p1", "w2:p1"}; !reflect.DeepEqual(status, want) {
		t.Fatalf("status panes = %v, want %v", status, want)
	}
	wantLife := []string{herdr.SubPaneAgentDetected, herdr.SubPaneClosed, herdr.SubPaneCreated, herdr.SubPaneExited, herdr.SubPaneMoved}
	if !reflect.DeepEqual(lifecycle, wantLife) {
		t.Fatalf("lifecycle = %v, want %v", lifecycle, wantLife)
	}
}

func TestHerdrSourceSubscribeRetriesRejectedPane(t *testing.T) {
	f := newFakeHerdr(t)
	f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusIdle))
	f.subErrs = []string{"not_found"} // a pane vanished between read and subscribe
	st, err := NewHerdrSource(f.path).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if n := len(f.subscribeRequests()); n != 2 {
		t.Fatalf("subscribe attempts = %d, want 2", n)
	}
}

func TestHerdrSourceStatusEventBecomesFullUpsert(t *testing.T) {
	f := newFakeHerdr(t)
	f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusWorking))
	src := NewHerdrSource(f.path)
	st, err := src.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := src.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}

	// herdr's event names only a pane and a status; the adapter must re-read.
	f.mu.Lock()
	f.agents["w1:p1"] = info("w1:p1", "term_a", herdr.AgentStatusBlocked)
	f.mu.Unlock()
	f.push <- `{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1","workspace_id":"w1","agent_status":"blocked"}}`

	ev := nextEvent(t, st)
	if ev.Kind != AgentUpsert || ev.Agent.ID != "term_a" || ev.Agent.Status != "blocked" || ev.Agent.Cwd != "/work/term_a" {
		t.Fatalf("event = %+v, want a full upsert of term_a as blocked", ev)
	}
}

func TestHerdrSourceClosedPaneBecomesRemovalByTerminalID(t *testing.T) {
	f := newFakeHerdr(t)
	f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusWorking))
	src := NewHerdrSource(f.path)
	st, err := src.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := src.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Underscored lifecycle name, pane id only: the cache is keyed by terminal id.
	f.push <- `{"event":"pane_closed","data":{"type":"pane_closed","pane_id":"w1:p1","workspace_id":"w1"}}`
	ev := nextEvent(t, st)
	if ev.Kind != AgentRemoved || ev.Agent.ID != "term_a" {
		t.Fatalf("event = %+v, want AgentRemoved{term_a}", ev)
	}
	// An unknown pane yields nothing; the next real event still arrives.
	f.push <- `{"event":"pane_closed","data":{"pane_id":"w9:p9","workspace_id":"w9"}}`
	f.push <- `{"event":"pane_created","data":{"pane":{"pane_id":"w1:p3"}}}`
	if ev := nextEvent(t, st); ev.Kind != Resync {
		t.Fatalf("event = %+v, want Resync for pane_created", ev)
	}
}

func TestHerdrSourceTopologyEventsAskForResync(t *testing.T) {
	for _, name := range []string{"pane_created", "pane.created", "pane_moved", "pane_agent_detected"} {
		t.Run(name, func(t *testing.T) {
			f := newFakeHerdr(t)
			f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusIdle))
			st, err := NewHerdrSource(f.path).Subscribe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			f.push <- `{"event":"` + name + `","data":{"pane_id":"w1:p1"}}`
			if ev := nextEvent(t, st); ev.Kind != Resync {
				t.Fatalf("event = %+v, want Resync", ev)
			}
		})
	}
}

func TestHerdrSourceStatusEventForVanishedAgentRemovesIt(t *testing.T) {
	f := newFakeHerdr(t)
	f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusWorking))
	src := NewHerdrSource(f.path)
	st, err := src.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := src.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	delete(f.agents, "w1:p1") // agent.get now answers not_found
	f.mu.Unlock()
	f.push <- `{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1","agent_status":"idle"}}`
	if ev := nextEvent(t, st); ev.Kind != AgentRemoved || ev.Agent.ID != "term_a" {
		t.Fatalf("event = %+v, want AgentRemoved{term_a}", ev)
	}
}

// protocol 22: events_lost is an error response carrying the subscribe request
// id, followed by the server closing the connection; it is not an event.
func TestHerdrSourceEventsLostErrorLine(t *testing.T) {
	f := newFakeHerdr(t)
	f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusIdle))
	st, err := NewHerdrSource(f.path).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	f.push <- `{"id":"sub1","error":{"code":"events_lost","message":"lost"}}`
	close(f.push)
	if ev := nextEvent(t, st); ev.Kind != EventsLost {
		t.Fatalf("event = %+v, want EventsLost", ev)
	}
	select {
	case _, ok := <-st.Events():
		if ok {
			t.Fatal("unexpected event after EventsLost")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not close after the server hung up")
	}
	if st.Err() == nil {
		t.Fatal("Err should be set once the server closed the stream")
	}
}

// A pane created between the pane listing and herdr's ack has neither a
// lifecycle event nor a status subscription, so Subscribe must list again after
// the ack and retry when the pane set moved.
func TestHerdrSourceSubscribeRetriesWhenPanesChangeDuringSubscribe(t *testing.T) {
	f := newFakeHerdr(t)
	p1, p2 := info("w1:p1", "term_a", herdr.AgentStatusIdle), info("w1:p2", "term_b", herdr.AgentStatusIdle)
	// 1st read (before subscribing): one pane. Every later read: two panes.
	f.setAgents(p1, p2)
	f.mu.Lock()
	f.snapSeq = [][]herdr.AgentInfo{{p1}, {p1, p2}}
	f.mu.Unlock()
	st, err := NewHerdrSource(f.path).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	reqs := f.subscribeRequests()
	if len(reqs) != 2 {
		t.Fatalf("subscribe requests = %d, want 2 (retry after the pane set changed)", len(reqs))
	}
	var panes []string
	for _, sp := range reqs[1].Subscriptions {
		if sp.Type == herdr.SubPaneAgentStatusChanged {
			panes = append(panes, sp.PaneID)
		}
	}
	sort.Strings(panes)
	if want := []string{"w1:p1", "w1:p2"}; !reflect.DeepEqual(panes, want) {
		t.Fatalf("final subscription covers %v, want %v", panes, want)
	}
}

// If the pane set never settles, the stream still starts but asks the loop to
// resync straight away rather than silently missing agents.
func TestHerdrSourceSubscribeGivesUpWithResync(t *testing.T) {
	f := newFakeHerdr(t)
	a := info("w1:p1", "term_a", herdr.AgentStatusIdle)
	b := info("w1:p2", "term_b", herdr.AgentStatusIdle)
	f.setAgents(a, b)
	f.mu.Lock()
	// pre-read and post-ack read always disagree
	f.snapSeq = [][]herdr.AgentInfo{{a}, {a, b}, {a}, {a, b}, {a}, {a, b}}
	f.mu.Unlock()
	st, err := NewHerdrSource(f.path).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if ev := nextEvent(t, st); ev.Kind != Resync {
		t.Fatalf("first event = %+v, want Resync", ev)
	}
}

func TestHerdrSourceTransientRereadFailureAsksForResync(t *testing.T) {
	f := newFakeHerdr(t)
	f.setAgents(info("w1:p1", "term_a", herdr.AgentStatusWorking))
	st, err := NewHerdrSource(f.path).Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	f.mu.Lock()
	f.getErr = "internal_error"
	f.mu.Unlock()
	f.push <- `{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1","agent_status":"idle"}}`
	if ev := nextEvent(t, st); ev.Kind != Resync {
		t.Fatalf("event = %+v, want Resync when the re-read fails", ev)
	}
}
