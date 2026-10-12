package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/model"
)

type ringEntry struct {
	id   uint64
	data []byte // JSON of the model.Change
}

// ring keeps the newest changes with contiguous, increasing ids so a
// reconnecting SSE client can replay what it missed.
type ring struct {
	mu      sync.Mutex
	cap     int
	entries []ringEntry // oldest first
	last    uint64      // id of the newest entry (the base when empty)
	waiters map[chan struct{}]struct{}
}

// newRing starts ids above a time-derived base so ids from a previous hub
// process are never mistaken for ids in this one.
func newRing(capacity int, start time.Time) *ring {
	var base uint64
	if ms := start.UnixMilli(); ms > 0 {
		base = uint64(ms) * 1000
	}
	return &ring{cap: capacity, last: base, waiters: map[chan struct{}]struct{}{}}
}

func (g *ring) add(c model.Change) {
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	g.mu.Lock()
	g.last++
	g.entries = append(g.entries, ringEntry{id: g.last, data: data})
	if len(g.entries) > g.cap {
		n := copy(g.entries, g.entries[len(g.entries)-g.cap:])
		g.entries = g.entries[:n]
	}
	for ch := range g.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	g.mu.Unlock()
}

// reset forgets every retained entry and wakes the waiting streams. Ids keep
// increasing, so any cursor issued before the reset is older than the ring and
// gets a snapshot instead of a replay.
func (g *ring) reset() {
	g.mu.Lock()
	g.entries = nil
	g.last++
	for ch := range g.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	g.mu.Unlock()
}

func (g *ring) lastID() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

// since returns the entries with id > after. ok is false when after is newer
// than anything issued (e.g. from a previous process) or older than the oldest
// retained entry minus one, i.e. some entries it would need are gone.
func (g *ring) since(after uint64) (out []ringEntry, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	oldest := g.last - uint64(len(g.entries)) // id just before the first entry
	if after > g.last || after < oldest {
		return nil, false
	}
	out = append(out, g.entries[after-oldest:]...)
	return out, true
}

func (g *ring) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	g.mu.Lock()
	g.waiters[ch] = struct{}{}
	g.mu.Unlock()
	return ch, func() {
		g.mu.Lock()
		delete(g.waiters, ch)
		g.mu.Unlock()
	}
}

// pump feeds engine changes into the ring until ctx ends. The engine closes a
// subscription whose buffer overflowed; the pump then resubscribes and resets
// the ring, which makes every connected client resync with a fresh snapshot
// (their cursors are older than anything the ring now holds).
func (s *server) pump(ch <-chan model.Change) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case c, ok := <-ch:
			if ok {
				s.ring.add(c)
				continue
			}
			if s.ctx.Err() != nil {
				return
			}
			slog.Warn("api: engine dropped the event subscription; resubscribing")
			ch = s.eng.Subscribe(s.ctx)
			s.ring.reset()
		}
	}
}

type snapshot struct {
	Sessions []model.SessionInfo `json:"sessions"`
	Agents   []model.Agent       `json:"agents"`
}

func (s *server) snapshotJSON(includeUnknown bool) []byte {
	sess := s.eng.Sessions()
	if sess == nil {
		sess = []model.SessionInfo{}
	}
	b, _ := json.Marshal(snapshot{Sessions: sess, Agents: filterAgents(s.eng.Agents(""), includeUnknown)})
	return b
}

func writeEvent(w http.ResponseWriter, event string, id uint64, data []byte) error {
	var buf bytes.Buffer
	buf.WriteString("event: " + event + "\n")
	buf.WriteString("id: " + strconv.FormatUint(id, 10) + "\n")
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	_, err := w.Write(buf.Bytes())
	return err
}

func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // streams outlive any server WriteTimeout
	includeUnknown := r.URL.Query().Get("include_unknown") == "1"

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}

	wake, unsub := s.ring.subscribe()
	defer unsub()

	var cursor uint64
	haveCursor := false
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			cursor, haveCursor = id, true
		}
	}

	tick := time.NewTicker(s.heartbeat)
	defer tick.Stop()

	for {
		var entries []ringEntry
		ok := false
		if haveCursor {
			entries, ok = s.ring.since(cursor)
		}
		if !ok {
			// Fresh connection, unusable Last-Event-ID, or the client fell
			// behind the ring: resync with a snapshot. Capture the id first so
			// anything after it is replayed (duplicates are harmless).
			cursor = s.ring.lastID()
			haveCursor = true
			if writeEvent(w, "snapshot", cursor, s.snapshotJSON(includeUnknown)) != nil || rc.Flush() != nil {
				return
			}
			continue
		}
		for _, e := range entries {
			if writeEvent(w, "change", e.id, e.data) != nil {
				return
			}
			cursor = e.id
		}
		if len(entries) > 0 && rc.Flush() != nil {
			return
		}

		select {
		case <-wake:
		case <-tick.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		}
	}
}
