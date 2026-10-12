package sessions

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// newRoot makes a short-pathed root (macOS limits unix socket paths).
func newRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "hs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// listen creates a real unix socket at path; closing the returned func
// closes the listener and removes the socket file.
func listen(t *testing.T, path string) func() {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	closeFn := func() {
		if closed {
			return
		}
		closed = true
		_ = l.Close() // removes the socket file
	}
	t.Cleanup(closeFn)
	return closeFn
}

func sess(root, name string) string { return filepath.Join(root, "sessions", name, "herdr.sock") }

func TestDiscoverMissingSessionsDir(t *testing.T) {
	root := newRoot(t)
	got, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}
}

func TestDiscoverDefaultOptional(t *testing.T) {
	root := newRoot(t)
	listen(t, sess(root, "CEKAT"))

	got, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Found{{"CEKAT", sess(root, "CEKAT")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	listen(t, filepath.Join(root, "herdr.sock"))
	got, err = Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "default" && got[0].Name != "default" {
		t.Fatalf("default socket not found: %v", got)
	}
	for _, f := range got {
		if f.Name == "default" && f.SocketPath != filepath.Join(root, "herdr.sock") {
			t.Fatalf("bad default path %v", f)
		}
	}
}

func TestDiscoverFilters(t *testing.T) {
	root := newRoot(t)
	listen(t, sess(root, "CEKAT"))
	dir := filepath.Join(root, "sessions", "CEKAT")
	listen(t, filepath.Join(dir, "herdr-client.sock"))
	if err := os.Symlink("/var/run/does-not-exist", filepath.Join(dir, "herdr.sock.agent")); err != nil {
		t.Fatal(err)
	}

	// session dir with a regular file named herdr.sock
	reg := sess(root, "REGULAR")
	if err := os.MkdirAll(filepath.Dir(reg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// session dir whose herdr.sock is a dangling symlink
	dang := sess(root, "DANGLING")
	if err := os.MkdirAll(filepath.Dir(dang), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent", dang); err != nil {
		t.Fatal(err)
	}
	// session dir without any socket, and a stray file in sessions/
	if err := os.MkdirAll(filepath.Join(root, "sessions", "EMPTY"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sessions", "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// root-level non-sockets
	if err := os.WriteFile(filepath.Join(root, "herdr.sock.agent"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	listen(t, filepath.Join(root, "herdr-client.sock"))

	got, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Found{{"CEKAT", sess(root, "CEKAT")}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestDiscoverSorted(t *testing.T) {
	root := newRoot(t)
	for _, n := range []string{"FRIDAY", "CEKAT", "alpha", "BRAVO"} {
		listen(t, sess(root, n))
	}
	listen(t, filepath.Join(root, "herdr.sock"))

	got, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got {
		names = append(names, f.Name)
	}
	want := []string{"BRAVO", "CEKAT", "FRIDAY", "alpha", "default"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v want %v", names, want)
	}
}

func recv(t *testing.T, ch <-chan Change, within time.Duration) (Change, bool) {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		return c, true
	case <-time.After(within):
		return Change{}, false
	}
}

func TestDiscoverWatchInitialAdded(t *testing.T) {
	root := newRoot(t)
	listen(t, sess(root, "FRIDAY"))
	listen(t, sess(root, "CEKAT"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := Watch(ctx, root, 20*time.Millisecond)

	for _, name := range []string{"CEKAT", "FRIDAY"} {
		c, ok := recv(t, ch, 2*time.Second)
		if !ok {
			t.Fatalf("timeout waiting for initial %s", name)
		}
		if c.Kind != Added || c.Name != name || c.SocketPath != sess(root, name) {
			t.Fatalf("got %+v", c)
		}
	}
	if c, ok := recv(t, ch, 100*time.Millisecond); ok {
		t.Fatalf("unexpected extra change %+v", c)
	}
}

func TestDiscoverWatchAddedRemoved(t *testing.T) {
	root := newRoot(t)
	const interval = 30 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := Watch(ctx, root, interval)

	if c, ok := recv(t, ch, 3*interval); ok {
		t.Fatalf("unexpected initial %+v", c)
	}

	closeFn := listen(t, sess(root, "NEW"))
	c, ok := recv(t, ch, 2*interval+500*time.Millisecond)
	if !ok || c.Kind != Added || c.Name != "NEW" {
		t.Fatalf("want Added NEW, got %+v ok=%v", c, ok)
	}

	closeFn()
	c, ok = recv(t, ch, 2*interval+500*time.Millisecond)
	if !ok || c.Kind != Removed || c.Name != "NEW" {
		t.Fatalf("want Removed NEW, got %+v ok=%v", c, ok)
	}

	// recreated after removal -> Added again
	listen(t, sess(root, "NEW"))
	c, ok = recv(t, ch, 2*interval+500*time.Millisecond)
	if !ok || c.Kind != Added || c.Name != "NEW" {
		t.Fatalf("want re-Added NEW, got %+v ok=%v", c, ok)
	}
}

func TestDiscoverWatchCancelClosesNoLeak(t *testing.T) {
	root := newRoot(t)
	listen(t, sess(root, "CEKAT"))

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ch := Watch(ctx, root, 10*time.Millisecond)
	// Never read the initial Added: cancel must still unblock the sender.
	time.Sleep(50 * time.Millisecond)
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				goto closed
			}
		case <-deadline:
			t.Fatal("channel not closed after cancel")
		}
	}
closed:
	for i := 0; i < 100 && runtime.NumGoroutine() > before; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("goroutine leak: before=%d after=%d", before, n)
	}
}

func TestDiscoverDefaultRoot(t *testing.T) {
	t.Setenv("HERDR_CONFIG_DIR", "/x/y")
	if got := DefaultRoot(); got != "/x/y" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("HERDR_CONFIG_DIR", "")
	home, _ := os.UserHomeDir()
	if got := DefaultRoot(); got != filepath.Join(home, ".config", "herdr") {
		t.Fatalf("got %q", got)
	}
}
