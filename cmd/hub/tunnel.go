package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
	"github.com/davidsugianto/herdr-session-hub/internal/tunnel"
)

// tunnelStore is what the tunnel command needs from the database.
type tunnelStore interface {
	store.SettingsStore
	CreatePairCode(ctx context.Context, ttl time.Duration) (string, error)
	Close() error
}

// Seams replaced in tests.
var (
	openTunnelStore = func(path string) (tunnelStore, error) {
		st, err := store.Open(path)
		if err != nil {
			return nil, err
		}
		ts, ok := any(st).(tunnelStore)
		if !ok {
			_ = st.Close()
			return nil, errors.New("database does not support settings")
		}
		return ts, nil
	}
	tunnelRunner  tunnel.Runner = tunnel.ExecRunner{}
	tunnelFindCLI               = func() (string, error) {
		return tunnel.FindCLI(exec.LookPath, func(p string) error {
			_, err := os.Stat(p)
			return err
		})
	}
)

// runTunnel exposes the hub over Tailscale HTTPS ("hub tunnel") or removes
// that exposure ("hub tunnel off").
func runTunnel(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	off := len(args) > 0 && args[0] == "off"
	if off {
		args = args[1:]
	}
	fs := flag.NewFlagSet("tunnel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", store.DefaultPath(), "SQLite database path (same as hub serve)")
	listen := fs.String("listen", "127.0.0.1:7878", "address hub serve listens on")
	ttl := fs.Duration("ttl", 10*time.Minute, "how long the pairing code stays valid")
	noQR := fs.Bool("no-qr", false, "print the pairing link only")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}

	cli, cliErr := tunnelFindCLI()
	if cliErr != nil && !off {
		return fail(cliErr)
	}
	st, err := openTunnelStore(*dbPath)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = st.Close() }()

	if off {
		// Without the CLI the saved settings (and the identity rule they
		// enable) are still cleared; only the serve config is left behind.
		if cliErr == nil {
			if err := tunnel.Down(ctx, tunnelRunner, cli); err != nil {
				return fail(err)
			}
		}
		// DeleteSetting of a missing key is not an error.
		for _, k := range []string{store.SettingTunnelHost, store.SettingTunnelLogin} {
			if err := st.DeleteSetting(ctx, k); err != nil {
				return fail(err)
			}
		}
		if cliErr != nil {
			_, _ = fmt.Fprintln(stderr, "hub: warning: tailscale CLI not found; cleared the saved tunnel settings but could not run `tailscale serve off`; remove the serve config yourself")
			return 0
		}
		_, _ = fmt.Fprintln(stdout, "tunnel off")
		return 0
	}

	if _, _, err := net.SplitHostPort(*listen); err != nil {
		return fail(fmt.Errorf("invalid -listen %q: %w", *listen, err))
	}
	info, err := tunnel.Status(ctx, tunnelRunner, cli)
	if err != nil {
		return fail(err)
	}
	if err := tunnel.Up(ctx, tunnelRunner, cli, "http://"+*listen); err != nil {
		return fail(err)
	}
	if err := st.SetSetting(ctx, store.SettingTunnelHost, info.Host); err != nil {
		return fail(err)
	}
	if err := st.SetSetting(ctx, store.SettingTunnelLogin, info.Login); err != nil {
		return fail(err)
	}
	code, err := st.CreatePairCode(ctx, *ttl)
	if err != nil {
		return fail(err)
	}
	remote := "https://" + info.Host
	_, _ = fmt.Fprintf(stdout, "remote URL: %s (tailnet only, owner %s)\n", remote, info.Login)
	printPairing(stdout, code, *ttl, remote, *noQR)
	return 0
}
