// Package macos holds macOS-only conveniences, currently the keep-awake
// assertion that stops the Mac sleeping while agents are working.
package macos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/model"
)

// DefaultGrace is how long the assertion is kept after the last agent stops
// working, so quick working/waiting flaps do not churn caffeinate.
const DefaultGrace = 20 * time.Second

// resubscribeDelay avoids a hot loop if the engine keeps closing the channel.
const defaultResubscribeDelay = 250 * time.Millisecond

// Engine is the part of model.Engine that keep-awake needs.
type Engine interface {
	Subscribe(ctx context.Context) <-chan model.Change
	Agents(session string) []model.Agent
}

// Options configures Run.
type Options struct {
	// Mode is "off", "while-working" or "always".
	Mode   string
	Engine Engine
	// Grace is how long to keep holding after the last working agent stops
	// (while-working only). Zero means DefaultGrace.
	Grace time.Duration
	// Start takes the assertion for pid and returns a function that releases
	// it. Nil means `caffeinate -i -w <pid>` (macOS only).
	Start func(ctx context.Context, pid int) (stop func(), err error)
	// Log receives diagnostics; nil discards them.
	Log io.Writer

	// resubscribeDelay is only overridden by tests.
	resubscribeDelay time.Duration
}

// Run blocks until ctx is done, holding a keep-awake assertion according to
// o.Mode, and releases it before returning.
func Run(ctx context.Context, o Options) {
	logf := func(format string, args ...any) {
		if o.Log != nil {
			_, _ = fmt.Fprintf(o.Log, "keep-awake: "+format+"\n", args...)
		}
	}
	if o.Mode == "off" || o.Mode == "" {
		return
	}
	if o.Mode != "while-working" && o.Mode != "always" {
		logf("unknown mode %q, disabled", o.Mode)
		return
	}
	if o.Start == nil {
		if runtime.GOOS != "darwin" {
			logf("%v", errUnsupported)
			return
		}
		o.Start = caffeinate
	}
	if o.Grace <= 0 {
		o.Grace = DefaultGrace
	}
	if o.resubscribeDelay <= 0 {
		o.resubscribeDelay = defaultResubscribeDelay
	}

	h := &holder{ctx: ctx, start: o.Start, logf: logf, pid: os.Getpid()}
	defer h.release()

	if o.Mode == "always" {
		if err := h.acquire(); err != nil {
			return // already logged; nothing else to do
		}
		<-ctx.Done()
		return
	}

	graceTimer := time.NewTimer(time.Hour)
	if !graceTimer.Stop() {
		<-graceTimer.C
	}
	defer graceTimer.Stop()
	graceArmed := false

	// reconcile recounts working agents and acquires or schedules release.
	reconcile := func() {
		if workingCount(o.Engine) > 0 {
			if graceArmed {
				if !graceTimer.Stop() {
					select {
					case <-graceTimer.C:
					default:
					}
				}
				graceArmed = false
			}
			_ = h.acquire()
			return
		}
		if h.held() && !graceArmed {
			graceTimer.Reset(o.Grace)
			graceArmed = true
		}
	}

	for ctx.Err() == nil {
		ch := o.Engine.Subscribe(ctx)
		reconcile() // after subscribing, so nothing slips between count and subscribe
	events:
		for {
			select {
			case <-ctx.Done():
				return
			case <-graceTimer.C:
				graceArmed = false
				if workingCount(o.Engine) == 0 {
					h.release()
				}
			case _, ok := <-ch:
				if !ok {
					break events
				}
				reconcile()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(o.resubscribeDelay):
		}
	}
}

func workingCount(e Engine) int {
	n := 0
	for _, a := range e.Agents("") {
		if a.State == model.StateWorking && a.Online {
			n++
		}
	}
	return n
}

// holder owns the single live assertion.
type holder struct {
	ctx   context.Context
	start func(ctx context.Context, pid int) (func(), error)
	logf  func(format string, args ...any)
	pid   int
	stop  func()
	// failLogged is set once a start failure has been logged, so retries on
	// every engine change stay quiet until a start succeeds again.
	failLogged bool
}

func (h *holder) held() bool { return h.stop != nil }

func (h *holder) acquire() error {
	if h.stop != nil {
		return nil
	}
	stop, err := h.start(h.ctx, h.pid)
	if err != nil {
		if !h.failLogged {
			h.logf("start failed: %v", err)
			h.failLogged = true
		}
		return err
	}
	h.failLogged = false
	h.stop = stop
	return nil
}

func (h *holder) release() {
	if h.stop != nil {
		h.stop()
		h.stop = nil
	}
}

var errUnsupported = errors.New("keep-awake is only supported on macOS")

// caffeinate runs `caffeinate -i -w <pid>`: prevent idle sleep for as long as
// the hub process lives, or until stop is called.
func caffeinate(ctx context.Context, pid int) (func(), error) {
	if runtime.GOOS != "darwin" {
		return nil, errUnsupported
	}
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, "caffeinate", "-i", "-w", fmt.Sprint(pid))
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start caffeinate: %w", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return func() { cancel(); <-done }, nil
}
