package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/config"
	"github.com/davidsugianto/herdr-session-hub/internal/sessions"
)

// formatUpdate renders one loop update as a single line. label maps a herdr
// session name (e.g. "CEKAT") to its display label (e.g. "work").
func formatUpdate(u sessions.Update, label func(string) string) string {
	prefix := fmt.Sprintf("%s [%s] ", u.At.Format("15:04:05"), label(u.Session))
	agent := fmt.Sprintf("%s %s", u.Agent.Kind, u.Agent.PaneID)
	if u.Agent.Name != "" {
		agent = fmt.Sprintf("%s %s (%s)", u.Agent.Kind, u.Agent.Name, u.Agent.PaneID)
	}
	switch u.Type {
	case sessions.UpdateOnline:
		return prefix + "online"
	case sessions.UpdateOffline:
		return fmt.Sprintf("%soffline: %v", prefix, u.Err)
	case sessions.UpdateAdded:
		return fmt.Sprintf("%s+ %s  %s", prefix, agent, u.Agent.Status)
	case sessions.UpdateRemoved:
		return fmt.Sprintf("%s- %s", prefix, agent)
	case sessions.UpdateTransition:
		return fmt.Sprintf("%s%s  %s -> %s", prefix, agent, u.From, u.To)
	}
	return prefix + string(u.Type)
}

func runWatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", sessions.DefaultRoot(), "herdr config dir containing sessions/")
	cfgPath := fs.String("config", config.DefaultPath(), "optional hub config.toml (session labels)")
	interval := fs.Duration("interval", 500*time.Millisecond, "how often to look for new or removed sessions")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	updates := make(chan sessions.Update, 64)
	var wg sync.WaitGroup
	running := map[string]context.CancelFunc{}
	start := func(f sessions.Found) {
		if _, ok := running[f.Name]; ok {
			return
		}
		lctx, lcancel := context.WithCancel(ctx)
		running[f.Name] = lcancel
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessions.Run(lctx, f.Name, sessions.NewHerdrSource(f.SocketPath), updates, sessions.Options{})
		}()
	}

	_, _ = fmt.Fprintf(stdout, "watching %s (ctrl-c to stop)\n", *root)
	changes := sessions.Watch(ctx, *root, *interval)
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return 0
		case c, ok := <-changes:
			if !ok {
				wg.Wait()
				return 0
			}
			switch c.Kind {
			case sessions.Added:
				_, _ = fmt.Fprintf(stdout, "session %s found at %s\n", cfg.Label(c.Name), c.SocketPath)
				start(c.Found)
			case sessions.Removed:
				_, _ = fmt.Fprintf(stdout, "session %s socket removed\n", cfg.Label(c.Name))
				if cancelLoop, ok := running[c.Name]; ok {
					cancelLoop()
					delete(running, c.Name)
				}
			}
		case u := <-updates:
			_, _ = fmt.Fprintln(stdout, formatUpdate(u, cfg.Label))
		}
	}
}
