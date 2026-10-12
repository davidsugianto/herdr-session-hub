// Package sessions discovers and tracks herdr sessions by their Unix sockets.
package sessions

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// DefaultName is the Name given to the optional default socket
	// (<root>/herdr.sock).
	DefaultName = "default"

	socketFile = "herdr.sock"
	sessionDir = "sessions"

	defaultInterval = 500 * time.Millisecond
)

// Found is a discovered herdr session socket.
type Found struct {
	// Name is the directory name under sessions/ (case-sensitive, e.g. "CEKAT"),
	// or "default" for <root>/herdr.sock.
	Name       string
	SocketPath string
}

// ChangeKind describes how a session changed between two polls.
type ChangeKind int

const (
	Added ChangeKind = iota
	Removed
)

func (k ChangeKind) String() string {
	switch k {
	case Added:
		return "added"
	case Removed:
		return "removed"
	}
	return "unknown"
}

// Change is a session appearing or disappearing.
type Change struct {
	Kind ChangeKind
	Found
}

// DefaultRoot returns $HERDR_CONFIG_DIR if set, else ~/.config/herdr.
func DefaultRoot() string {
	if d := os.Getenv("HERDR_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "herdr")
	}
	return filepath.Join(home, ".config", "herdr")
}

// isSocket reports whether path (following symlinks) is a Unix socket.
// Regular files, directories, dangling links and links to non-sockets are not.
func isSocket(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// Discover lists the herdr sockets under root, sorted by Name.
//
// Sessions live at <root>/sessions/<name>/herdr.sock. The default socket
// <root>/herdr.sock is optional. Only paths that are Unix sockets are
// returned; other files (e.g. herdr.sock.agent, herdr-client.sock) are
// ignored. A missing sessions/ directory is not an error.
func Discover(root string) ([]Found, error) {
	var out []Found

	if p := filepath.Join(root, socketFile); isSocket(p) {
		out = append(out, Found{Name: DefaultName, SocketPath: p})
	}

	entries, err := os.ReadDir(filepath.Join(root, sessionDir))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		p := filepath.Join(root, sessionDir, e.Name(), socketFile)
		if isSocket(p) {
			out = append(out, Found{Name: e.Name(), SocketPath: p})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Watch polls root every interval (500ms if interval <= 0) and reports
// changes on the returned channel. It first emits Added for every session
// already present, then Removed/Added as sockets disappear/appear. The
// channel is closed when ctx is cancelled.
//
// Sessions are identified by (Name, SocketPath). A socket removed and
// recreated between two polls is indistinguishable from one that never
// went away (likewise a path whose file is replaced by a new inode), so
// no change is emitted; a removal observed in at least one poll yields
// Removed followed by Added when it returns. Transient Discover errors
// skip that poll. Within one poll Removed changes precede Added, each
// sorted by Name. Consumers must keep reading until close or cancel ctx.
func Watch(ctx context.Context, root string, interval time.Duration) <-chan Change {
	if interval <= 0 {
		interval = defaultInterval
	}
	ch := make(chan Change)

	go func() {
		defer close(ch)

		known := map[Found]bool{}
		poll := func() bool {
			cur, err := Discover(root)
			if err != nil {
				return true
			}
			now := make(map[Found]bool, len(cur))
			for _, f := range cur {
				now[f] = true
			}
			var removed []Found
			for f := range known {
				if !now[f] {
					removed = append(removed, f)
				}
			}
			sort.Slice(removed, func(i, j int) bool { return removed[i].Name < removed[j].Name })
			for _, f := range removed {
				if !send(ctx, ch, Change{Kind: Removed, Found: f}) {
					return false
				}
				delete(known, f)
			}
			for _, f := range cur {
				if known[f] {
					continue
				}
				if !send(ctx, ch, Change{Kind: Added, Found: f}) {
					return false
				}
				known[f] = true
			}
			return true
		}

		if !poll() {
			return
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !poll() {
					return
				}
			}
		}
	}()

	return ch
}

func send(ctx context.Context, ch chan<- Change, c Change) bool {
	select {
	case ch <- c:
		return true
	case <-ctx.Done():
		return false
	}
}
