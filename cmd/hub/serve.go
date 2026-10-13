package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/actions"
	"github.com/davidsugianto/herdr-session-hub/internal/api"
	"github.com/davidsugianto/herdr-session-hub/internal/config"
	"github.com/davidsugianto/herdr-session-hub/internal/macos"
	"github.com/davidsugianto/herdr-session-hub/internal/model"
	"github.com/davidsugianto/herdr-session-hub/internal/notify"
	"github.com/davidsugianto/herdr-session-hub/internal/profiles"
	"github.com/davidsugianto/herdr-session-hub/internal/sessions"
	"github.com/davidsugianto/herdr-session-hub/internal/store"
	"github.com/davidsugianto/herdr-session-hub/internal/webui"
	"github.com/davidsugianto/herdr-session-hub/web"
)

// socketTable is the live session name -> socket path map shared by the
// discovery loop (writer) and the action executor (reader).
type socketTable struct {
	mu sync.RWMutex
	m  map[string]string
}

func (t *socketTable) set(name, path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[name] = path
}

func (t *socketTable) del(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, name)
}

// Socket implements actions.SocketResolver.
func (t *socketTable) Socket(session string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	p, ok := t.m[session]
	return p, ok
}

// serveOptions are the parsed `hub serve` flags.
type serveOptions struct {
	listen     string
	root       string
	cfgPath    string
	dbPath     string
	interval   time.Duration
	allowHosts []string
}

var errEmptyAllowHost = errors.New("-allow-host needs a host name")

// parseServeFlags parses the serve flags without binding anything. Flag
// diagnostics go to stderr, except an empty -allow-host, which is reported
// only through the returned error.
func parseServeFlags(args []string, stderr io.Writer) (serveOptions, error) {
	var o serveOptions
	var out strings.Builder
	var emptyHost bool // flag wraps errors from Func with %v, so errors.Is would not see ours
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(&out)
	fs.StringVar(&o.listen, "listen", "127.0.0.1:7878", "address to listen on (loopback only unless you pass something else on purpose)")
	fs.StringVar(&o.root, "root", sessions.DefaultRoot(), "herdr config dir containing sessions/")
	fs.StringVar(&o.cfgPath, "config", config.DefaultPath(), "optional hub config.toml (session labels)")
	fs.StringVar(&o.dbPath, "db", store.DefaultPath(), "SQLite database path")
	fs.DurationVar(&o.interval, "interval", 500*time.Millisecond, "how often to look for new or removed sessions")
	fs.Func("allow-host", "extra Host header name to accept besides loopback, e.g. my-mac.local or '*.ts.net' (repeatable)", func(v string) error {
		v = strings.TrimSpace(v)
		if v == "" {
			emptyHost = true
			return errEmptyAllowHost
		}
		o.allowHosts = append(o.allowHosts, v)
		return nil
	})
	err := fs.Parse(args)
	if err != nil {
		if emptyHost {
			return o, errEmptyAllowHost
		}
		_, _ = io.WriteString(stderr, out.String())
		return o, err
	}
	return o, nil
}

// listenAdvice returns the warning lines to print at startup for a listen
// address. Loopback binds produce none.
func listenAdvice(addr string, allowHosts []string) []string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	lines := []string{"hub: warning: not listening on loopback; the hub is reachable from the network and anyone who can reach this port can try to pair"}
	if len(allowHosts) == 0 {
		return append(lines, "hub: no -allow-host given: requests with a non-loopback Host header will be rejected with 403 (pass e.g. -allow-host '*.ts.net' or -allow-host my-mac.local)")
	}
	return append(lines, "hub: allowed Host names: "+strings.Join(allowHosts, ", ")+" (plus loopback)")
}

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseServeFlags(args, stderr)
	if err != nil {
		if errors.Is(err, errEmptyAllowHost) {
			_, _ = fmt.Fprintln(stderr, "hub:", err)
		}
		return 2
	}
	cfg, err := config.Load(opts.cfgPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	st, err := store.Open(opts.dbPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	reg, err := profiles.Load()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	engine := model.NewEngine(model.Options{Label: cfg.Label})
	sockets := &socketTable{m: map[string]string{}}
	exec := actions.New(sockets, reg)

	// Timeline: persist every engine change that is an agent state transition.
	startTimeline(ctx, engine, st, stderr)
	go pruneLoop(ctx, st, stderr)

	// Sessions: one sync loop per discovered socket, all feeding the engine.
	updates := make(chan sessions.Update, 64)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case u := <-updates:
				engine.Apply(u)
			}
		}
	}()
	var wg sync.WaitGroup
	go superviseSessions(ctx, &wg, opts.root, opts.interval, sockets, updates, engine, stdout, cfg)

	ln, err := net.Listen("tcp", opts.listen)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	notifier, err := notify.New(notify.Options{Engine: engine, Store: st, Prompts: exec, Quiet: quietPolicy{cfg},
		BlockedBreaksQuiet: cfg.Notify.BlockedBreaksQuiet, Label: cfg.Label, Contact: cfg.Notify.Contact, Log: stderr})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	go notifier.Run(ctx)
	go macos.Run(ctx, macos.Options{Mode: cfg.Mac.KeepAwake, Engine: engine, Log: stderr})

	handler := api.New(ctx, api.Config{Engine: engine, Store: st, Actions: exec, Secure: false, AllowedHosts: opts.allowHosts,
		Settings: st, Push: st, Mutes: st, Notifier: notifier, Quiet: quietRules(cfg), BlockedBreaksQuiet: cfg.Notify.BlockedBreaksQuiet,
		Static: webui.Handler(web.FS())})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	_, _ = fmt.Fprintf(stdout, "hub listening on http://%s (db %s)\n", ln.Addr(), opts.dbPath)
	_, _ = fmt.Fprintf(stdout, "open http://%s and pair with: hub pair\n", ln.Addr())
	for _, line := range listenAdvice(ln.Addr().String(), opts.allowHosts) {
		_, _ = fmt.Fprintln(stderr, line)
	}
	if host, err := st.Setting(ctx, store.SettingTunnelHost); err == nil && host != "" {
		_, _ = fmt.Fprintf(stdout, "remote https://%s (tailnet only, from `hub tunnel`)\n", host)
	} else {
		_, _ = fmt.Fprintln(stdout, "remote run `hub tunnel` to reach the hub from your iPhone over Tailscale")
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintln(stderr, "hub:", err)
			cancel()
			wg.Wait()
			return 1
		}
	}
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = srv.Shutdown(shutdown)
	cancel()
	wg.Wait()
	return 0
}

// superviseSessions runs one sessions.Run loop per discovered socket and marks
// a session offline in the engine when its socket disappears.
func superviseSessions(ctx context.Context, wg *sync.WaitGroup, root string, interval time.Duration,
	sockets *socketTable, updates chan<- sessions.Update, engine *model.Engine, stdout io.Writer, cfg config.Config) {
	running := map[string]context.CancelFunc{}
	for c := range sessions.Watch(ctx, root, interval) {
		switch c.Kind {
		case sessions.Added:
			if _, ok := running[c.Name]; ok {
				continue
			}
			_, _ = fmt.Fprintf(stdout, "session %s found at %s\n", cfg.Label(c.Name), c.SocketPath)
			sockets.set(c.Name, c.SocketPath)
			lctx, lcancel := context.WithCancel(ctx)
			running[c.Name] = lcancel
			wg.Add(1)
			go func(f sessions.Found) {
				defer wg.Done()
				sessions.Run(lctx, f.Name, sessions.NewHerdrSource(f.SocketPath), updates, sessions.Options{})
			}(c.Found)
		case sessions.Removed:
			_, _ = fmt.Fprintf(stdout, "session %s socket removed\n", cfg.Label(c.Name))
			if cancelLoop, ok := running[c.Name]; ok {
				cancelLoop()
				delete(running, c.Name)
			}
			sockets.del(c.Name)
			engine.Apply(sessions.Update{Session: c.Name, Type: sessions.UpdateOffline, At: time.Now(),
				Err: errors.New("socket removed")})
		}
	}
}

// startTimeline subscribes to the engine before returning (so no change made
// afterwards is missed) and records state changes in the background. If the
// engine drops the subscription because the store is too slow, it resubscribes;
// the changes lost in between are logged, not recoverable.
func startTimeline(ctx context.Context, engine *model.Engine, st store.Store, stderr io.Writer) {
	ch := engine.Subscribe(ctx)
	go func() {
		for {
			for c := range ch {
				recordChange(ctx, st, c, stderr)
			}
			if ctx.Err() != nil {
				return
			}
			_, _ = fmt.Fprintln(stderr, "hub: timeline: subscription dropped (store too slow); resubscribing, some events were not recorded")
			ch = engine.Subscribe(ctx)
		}
	}()
}

func recordChange(ctx context.Context, st store.Store, ch model.Change, stderr io.Writer) {
	var e store.Event
	switch ch.Type {
	case model.ChangeAdded:
		e = store.Event{From: "", To: string(ch.Agent.State)}
	case model.ChangeState:
		e = store.Event{From: string(ch.From), To: string(ch.To)}
	case model.ChangeRemoved:
		e = store.Event{From: string(ch.Agent.State), To: "removed"}
	default:
		return
	}
	e.Session, e.AgentID, e.Name, e.At, e.Source = ch.Session, ch.Agent.ID, ch.Agent.Name, ch.At, "herdr"
	if err := st.AddEvent(ctx, e); err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintln(stderr, "hub: timeline:", err)
	}
}

func pruneLoop(ctx context.Context, st store.Store, stderr io.Writer) {
	prune := func() {
		if err := st.Prune(ctx, time.Now().Add(-store.DefaultRetention)); err != nil && ctx.Err() == nil {
			_, _ = fmt.Fprintln(stderr, "hub: prune:", err)
		}
	}
	prune()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			prune()
		}
	}
}

// quietPolicy adapts config.Config to notify.QuietPolicy.
type quietPolicy struct{ cfg config.Config }

func (q quietPolicy) Active(session string, t time.Time) bool { return q.cfg.QuietActive(session, t) }

// quietRules lists the configured per-session quiet_hours for GET /api/notify.
func quietRules(cfg config.Config) []api.QuietRule {
	var out []api.QuietRule
	for _, s := range cfg.Sessions {
		if s.QuietHours != "" {
			out = append(out, api.QuietRule{Session: s.Name, Rule: s.QuietHours})
		}
	}
	return out
}
