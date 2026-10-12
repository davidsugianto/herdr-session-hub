package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
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

const (
	adviceReachable = "hub: warning: not listening on loopback; the hub is reachable from the network and anyone who can reach this port can try to pair"
	adviceNoAllow   = "hub: no -allow-host given: requests with a non-loopback Host header will be rejected with 403 (pass e.g. -allow-host '*.ts.net' or -allow-host my-mac.local)"
)

func TestListenAdvice(t *testing.T) {
	ts := []string{"*.ts.net"}
	multi := []string{"*.ts.net", "my-mac.local"}
	tests := []struct {
		name  string
		addr  string
		hosts []string
		want  []string
	}{
		{"loopback v4", "127.0.0.1:7878", nil, nil},
		{"loopback v4 other", "127.1.2.3:7878", nil, nil},
		{"loopback v6", "[::1]:7878", nil, nil},
		{"localhost", "localhost:7878", nil, nil},
		{"loopback with allow-hosts stays quiet", "127.0.0.1:7878", ts, nil},
		{"wildcard v4", "0.0.0.0:7878", nil, []string{adviceReachable, adviceNoAllow}},
		{"wildcard empty host", ":7878", nil, []string{adviceReachable, adviceNoAllow}},
		{"wildcard v6", "[::]:7878", nil, []string{adviceReachable, adviceNoAllow}},
		{"lan ip", "192.168.1.5:7878", nil, []string{adviceReachable, adviceNoAllow}},
		{"lan ip with suffix", "192.168.1.5:7878", ts, []string{adviceReachable, "hub: allowed Host names: *.ts.net (plus loopback)"}},
		{"wildcard with several", "0.0.0.0:7878", multi, []string{adviceReachable, "hub: allowed Host names: *.ts.net, my-mac.local (plus loopback)"}},
		{"empty host with allow", ":7878", ts, []string{adviceReachable, "hub: allowed Host names: *.ts.net (plus loopback)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listenAdvice(tt.addr, tt.hosts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("listenAdvice(%q, %v) = %q, want %q", tt.addr, tt.hosts, got, tt.want)
			}
		})
	}
}

func TestParseServeFlagsAllowHost(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		o, err := parseServeFlags(nil, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if o.listen != "127.0.0.1:7878" {
			t.Fatalf("listen = %q", o.listen)
		}
		if len(o.allowHosts) != 0 {
			t.Fatalf("allowHosts = %q, want none", o.allowHosts)
		}
	})
	t.Run("repeated single and double dash, trimmed", func(t *testing.T) {
		o, err := parseServeFlags([]string{"-allow-host", " my-mac.local ", "--allow-host", "*.ts.net", "-allow-host=other.example"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"my-mac.local", "*.ts.net", "other.example"}
		if !reflect.DeepEqual(o.allowHosts, want) {
			t.Fatalf("allowHosts = %q, want %q", o.allowHosts, want)
		}
	})
	for _, v := range []string{"", "   "} {
		t.Run(fmt.Sprintf("empty %q rejected", v), func(t *testing.T) {
			var buf bytes.Buffer
			_, err := parseServeFlags([]string{"-allow-host", v}, &buf)
			if err == nil {
				t.Fatal("want error")
			}
			if !errors.Is(err, errEmptyAllowHost) {
				t.Fatalf("err = %v, want errEmptyAllowHost", err)
			}
			if !strings.Contains(err.Error(), "-allow-host needs a host name") {
				t.Fatalf("err = %v", err)
			}
			if buf.Len() != 0 {
				t.Fatalf("flag usage leaked to stderr: %q", buf.String())
			}
		})
	}
	t.Run("other flags still parse", func(t *testing.T) {
		o, err := parseServeFlags([]string{"-listen", "0.0.0.0:9000", "-db", "/tmp/x.db"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if o.listen != "0.0.0.0:9000" || o.dbPath != "/tmp/x.db" {
			t.Fatalf("got %+v", o)
		}
	})
}
