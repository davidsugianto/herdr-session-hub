// Command hub is the herdr-session-hub spike CLI.
//
//	hub watch    print live agent state changes for every herdr session
//	hub capture  save a pane's text (e.g. a blocked prompt) as a fixture
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `usage: hub <command> [flags]

commands:
  watch     print live agent state changes for every herdr session
  capture   save a pane's text as a fixture: hub capture <session> <agent-or-pane>

run "hub <command> -h" for flags`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "watch":
		return runWatch(ctx, args[1:], stdout, stderr)
	case "capture":
		return runCapture(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		_, _ = fmt.Fprintln(stdout, usage)
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "hub: unknown command %q\n\n%s\n", args[0], usage)
	return 2
}
