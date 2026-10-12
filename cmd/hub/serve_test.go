package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/sessions"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

// evStore records AddEvent calls; the embedded nil Store panics if anything
// else is used.
type evStore struct {
	store.Store
	gate chan struct{} // AddEvent blocks until it is closed (nil: never blocks)
	mu   sync.Mutex
	got  []store.Event
}

func (s *evStore) AddEvent(_ context.Context, e store.Event) error {
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	s.got = append(s.got, e)
	s.mu.Unlock()
	return nil
}

func (s *evStore) has(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.got {
		if e.AgentID == agentID {
			return true
		}
	}
	return false
}

func addAgent(e *model.Engine, id string) {
	e.Apply(sessions.Update{Session: "S", Type: sessions.UpdateAdded, At: time.Now(),
		Agent: sessions.Agent{ID: id, PaneID: "w1:p1", Name: "claude", Kind: "claude", Status: "idle"}})
}

func waitUntil(t *testing.T, what string, cond func() bool) {
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

// The timeline recorder must see changes from the moment startTimeline returns,
// and keep recording after the engine drops it for being too slow.
func TestTimelineRecorderSurvivesSlowStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := model.NewEngine(model.Options{})
	st := &evStore{gate: make(chan struct{})}
	startTimeline(ctx, engine, st, io.Discard)

	addAgent(engine, "first")
	// Flood past the subscriber buffer while the store is blocked.
	for i := 0; i < 600; i++ {
		addAgent(engine, fmt.Sprintf("flood%d", i))
	}
	close(st.gate)

	// After the drop it must have resubscribed.
	waitUntil(t, "first event stored", func() bool { return st.has("S/first") })
	time.Sleep(100 * time.Millisecond)
	addAgent(engine, "after")
	waitUntil(t, "event after the drop stored", func() bool { return st.has("S/after") })
}
