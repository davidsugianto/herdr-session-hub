package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

// runPair prints a one-time pairing code (and a QR of the pairing URL). The
// running hub exchanges it for a device token at POST /api/pair.
func runPair(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", store.DefaultPath(), "SQLite database path (same as hub serve)")
	base := fs.String("url", "http://127.0.0.1:7878", "hub base URL to put in the QR code")
	ttl := fs.Duration("ttl", 10*time.Minute, "how long the code stays valid")
	noQR := fs.Bool("no-qr", false, "print the code only")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	code, err := st.CreatePairCode(ctx, *ttl)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "hub:", err)
		return 1
	}
	link := *base + "/#pair=" + url.QueryEscape(code)
	_, _ = fmt.Fprintf(stdout, "pairing code: %s (valid %s, single use)\n%s\n", code, *ttl, link)
	if !*noQR {
		qrterminal.GenerateHalfBlock(link, qrterminal.L, stdout)
	}
	_, _ = fmt.Fprintf(stdout, "exchange it for a token:\n  curl -s -X POST %s/api/pair -d '{\"code\":\"%s\",\"device_name\":\"curl\"}'\n", *base, code)
	return 0
}
