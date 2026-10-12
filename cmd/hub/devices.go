package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const devicesUsage = `usage: hub devices <subcommand> [flags]

subcommands:
  list     list paired devices (tokens are never shown)
  revoke   sign a device out immediately: hub devices revoke [-yes] <id>`

// stdin is where "hub devices revoke" reads its confirmation; tests override it.
var stdin io.Reader = os.Stdin

// runDevices lists and revokes paired devices in the hub's SQLite database.
func runDevices(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, devicesUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return runDevicesList(ctx, args[1:], stdout, stderr)
	case "revoke":
		return runDevicesRevoke(ctx, args[1:], stdin, stdout, stderr)
	}
	_, _ = fmt.Fprintf(stderr, "hub: unknown devices subcommand %q\n\n%s\n", args[0], devicesUsage)
	return 2
}

func runDevicesList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("devices list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", store.DefaultPath(), "SQLite database path (same as hub serve)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "usage: hub devices list [-db path]\n")
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	devs, err := st.Devices(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	if len(devs) == 0 {
		_, _ = fmt.Fprintln(stdout, "no paired devices")
		return 0
	}
	_, _ = fmt.Fprintln(stdout, "ID  NAME  CREATED  LAST SEEN")
	for _, d := range devs {
		_, _ = fmt.Fprintf(stdout, "%s  %s  created %s  last seen %s\n", d.ID, d.Name,
			d.CreatedAt.UTC().Format(time.RFC3339), d.LastSeen.UTC().Format(time.RFC3339))
	}
	return 0
}

func runDevicesRevoke(ctx context.Context, args []string, in io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("devices revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", store.DefaultPath(), "SQLite database path (same as hub serve)")
	yes := fs.Bool("yes", false, "revoke without asking for confirmation")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintf(stderr, "usage: hub devices revoke [-db path] [-yes] <id>\n")
		return 2
	}
	id := fs.Arg(0)
	st, err := store.Open(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	devs, err := st.Devices(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	var dev *store.Device
	for i := range devs {
		if devs[i].ID == id {
			dev = &devs[i]
			break
		}
	}
	if dev == nil {
		_, _ = fmt.Fprintf(stderr, "hub: no such device %s\n", id)
		return 1
	}
	if !*yes {
		_, _ = fmt.Fprintf(stdout, "Revoke device %s (%s)? This signs it out immediately. [y/N] ", dev.ID, dev.Name)
		line, _ := bufio.NewReader(in).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			_, _ = fmt.Fprintln(stdout)
			_, _ = fmt.Fprintln(stderr, "aborted")
			return 1
		}
	}
	if err := st.RevokeDevice(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			_, _ = fmt.Fprintf(stderr, "hub: no such device %s\n", id)
		} else {
			_, _ = fmt.Fprintln(stderr, "hub:", err)
		}
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "revoked %s\n", id)
	return 0
}
