package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/config"
	"github.com/davidsugianto/herdr-session-hub/internal/herdr"
	"github.com/davidsugianto/herdr-session-hub/internal/sessions"
)

// findSession resolves a session by herdr name or configured label.
func findSession(found []sessions.Found, cfg config.Config, want string) (sessions.Found, bool) {
	for _, f := range found {
		if f.Name == want || cfg.Label(f.Name) == want {
			return f, true
		}
	}
	return sessions.Found{}, false
}

// captureName is the fixture file name for a pane read.
func captureName(label, pane string, at time.Time) string {
	clean := strings.NewReplacer(":", "-", "/", "-", "\\", "-")
	return fmt.Sprintf("%s-%s-%s.txt", clean.Replace(label), clean.Replace(pane), at.UTC().Format("20060102T150405Z"))
}

func runCapture(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", sessions.DefaultRoot(), "herdr config dir containing sessions/")
	cfgPath := fs.String("config", config.DefaultPath(), "optional hub config.toml (session labels)")
	source := fs.String("source", string(herdr.ReadSourceRecentUnwrapped), "visible | recent | recent_unwrapped | detection")
	lines := fs.Uint("lines", 120, "how many lines to read")
	out := fs.String("out", filepath.Join("testdata", "screens"), "directory for the fixture")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: hub capture [flags] <session> <agent-name-or-pane-id>")
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	all, err := sessions.Discover(*root)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	sess, ok := findSession(all, cfg, fs.Arg(0))
	if !ok {
		_, _ = fmt.Fprintf(stderr, "hub: no session %q under %s\n", fs.Arg(0), *root)
		return 1
	}

	var agent herdr.AgentGetResult
	if err := herdr.CallOnce(ctx, sess.SocketPath, herdr.MethodAgentGet, herdr.AgentGetParams{Target: fs.Arg(1)}, &agent); err != nil {
		_, _ = fmt.Fprintf(stderr, "hub: resolve %q: %v\n", fs.Arg(1), err)
		return 1
	}
	n := uint32(*lines)
	var read herdr.PaneReadResult
	params := herdr.PaneReadParams{PaneID: agent.Agent.PaneID, Source: herdr.ReadSource(*source), Lines: &n}
	if err := herdr.CallOnce(ctx, sess.SocketPath, herdr.MethodPaneRead, params, &read); err != nil {
		_, _ = fmt.Fprintf(stderr, "hub: read pane %s: %v\n", agent.Agent.PaneID, err)
		return 1
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	path := filepath.Join(*out, captureName(cfg.Label(sess.Name), agent.Agent.PaneID, time.Now()))
	if err := os.WriteFile(path, []byte(read.Read.Text), 0o600); err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "%s  (%s, status %s, %d bytes)\n", path, agent.Agent.Agent, agent.Agent.AgentStatus, len(read.Read.Text))
	_, _ = fmt.Fprintln(stderr, "note: captured terminal text may contain secrets; review it before committing")
	return 0
}
