// Package tunnel exposes the hub over the user's tailnet by driving the
// Tailscale CLI (`tailscale serve`). The hub itself keeps listening on
// loopback; Tailscale terminates TLS and proxies to it.
package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// AppBundleCLI is where the macOS app ships its CLI when `tailscale` is not
// on PATH.
const AppBundleCLI = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// Runner runs a command and returns its combined stdout and stderr.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the real Runner.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// Tailscale CLI arguments live here and nowhere else.
//
// ASSUMPTION: these follow the `tailscale serve` syntax of recent releases
// (serve --bg --https=<port> <target>; serve --https=<port> off). Tailscale
// is not installed on the development machine, so they must be re-verified
// against `tailscale serve --help` on a machine that has it.
func statusArgs() []string { return []string{"status", "--json"} }

func upArgs(target string) []string {
	return []string{"serve", "--bg", "--https=443", target}
}

func downArgs() []string { return []string{"serve", "--https=443", "off"} }

// FindCLI locates the tailscale binary: PATH first, then the macOS app bundle.
func FindCLI(lookPath func(string) (string, error), stat func(string) error) (string, error) {
	if p, err := lookPath("tailscale"); err == nil {
		return p, nil
	}
	if stat(AppBundleCLI) == nil {
		return AppBundleCLI, nil
	}
	return "", errors.New("tailscale CLI not found: install Tailscale (https://tailscale.com/download) and sign in, then retry")
}

// Info describes this machine on the tailnet.
type Info struct {
	Host  string // MagicDNS name without the trailing dot
	Login string // Tailscale login name of the machine's owner
}

type statusJSON struct {
	Self *struct {
		DNSName string
		UserID  int64
	}
	User map[string]struct{ LoginName string }
}

// Status reads this machine's tailnet host name and owner login.
func Status(ctx context.Context, r Runner, cli string) (Info, error) {
	out, err := r.Run(ctx, cli, statusArgs()...)
	if err != nil {
		return Info{}, cmdErr("tailscale status", out, err)
	}
	var s statusJSON
	if err := json.Unmarshal(out, &s); err != nil {
		return Info{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	if s.Self == nil {
		return Info{}, errors.New("tailscale is not logged in: run `tailscale up` first")
	}
	host := strings.TrimSuffix(s.Self.DNSName, ".")
	if host == "" {
		return Info{}, errors.New("tailscale has no DNS name for this machine: enable MagicDNS and HTTPS certificates in the Tailscale admin console")
	}
	u, ok := s.User[strconv.FormatInt(s.Self.UserID, 10)]
	if !ok || u.LoginName == "" {
		return Info{}, errors.New("tailscale status has no login for this machine's user")
	}
	return Info{Host: host, Login: u.LoginName}, nil
}

// Up starts proxying https://<host> to target (e.g. http://127.0.0.1:7878).
func Up(ctx context.Context, r Runner, cli, target string) error {
	if out, err := r.Run(ctx, cli, upArgs(target)...); err != nil {
		return cmdErr("tailscale serve", out, err)
	}
	return nil
}

// Down stops the proxy started by Up.
func Down(ctx context.Context, r Runner, cli string) error {
	if out, err := r.Run(ctx, cli, downArgs()...); err != nil {
		return cmdErr("tailscale serve off", out, err)
	}
	return nil
}

func cmdErr(what string, out []byte, err error) error {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%s: %w: %s", what, err, msg)
	}
	return fmt.Errorf("%s: %w", what, err)
}
