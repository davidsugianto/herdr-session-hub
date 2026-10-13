package tunnel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type call struct {
	name string
	args []string
}

type fakeRunner struct {
	out   []byte
	err   error
	calls []call
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{name, args})
	return f.out, f.err
}

const statusOK = `{
  "BackendState": "Running",
  "Self": {"ID": "n1", "DNSName": "macbook.tail1234.ts.net.", "UserID": 4242},
  "User": {
    "4242": {"ID": 4242, "LoginName": "david@example.com", "DisplayName": "David"},
    "7": {"ID": 7, "LoginName": "other@example.com"}
  }
}`

func TestStatusOK(t *testing.T) {
	r := &fakeRunner{out: []byte(statusOK)}
	info, err := Status(context.Background(), r, "/bin/tailscale")
	if err != nil {
		t.Fatal(err)
	}
	if info.Host != "macbook.tail1234.ts.net" || info.Login != "david@example.com" {
		t.Fatalf("info = %+v", info)
	}
	want := []call{{"/bin/tailscale", []string{"status", "--json"}}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestStatusErrors(t *testing.T) {
	tests := []struct {
		name string
		out  string
		err  error
		want string
	}{
		{"no self", `{"BackendState":"NeedsLogin"}`, nil, "not logged in"},
		{"no self null", `{"Self":null}`, nil, "not logged in"},
		{"empty dns", `{"Self":{"DNSName":"","UserID":1},"User":{"1":{"LoginName":"a@b"}}}`, nil, "HTTPS certificates"},
		{"bad json", `nope`, nil, "parse"},
		{"no user", `{"Self":{"DNSName":"h.ts.net.","UserID":9},"User":{}}`, nil, "login"},
		{"cmd fail", "boom: denied\n", errors.New("exit status 1"), "boom: denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{out: []byte(tt.out), err: tt.err}
			_, err := Status(context.Background(), r, "ts")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestUpDown(t *testing.T) {
	r := &fakeRunner{}
	if err := Up(context.Background(), r, "ts", "http://127.0.0.1:7878"); err != nil {
		t.Fatal(err)
	}
	if err := Down(context.Background(), r, "ts"); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{"ts", []string{"serve", "--bg", "--https=443", "http://127.0.0.1:7878"}},
		{"ts", []string{"serve", "--https=443", "off"}},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %v", r.calls)
	}
}

func TestUpFailureIncludesStderr(t *testing.T) {
	r := &fakeRunner{out: []byte("  serve not enabled\n"), err: errors.New("exit status 1")}
	err := Up(context.Background(), r, "ts", "http://x")
	if err == nil || !strings.Contains(err.Error(), "serve not enabled") {
		t.Fatalf("err = %v", err)
	}
	err = Down(context.Background(), r, "ts")
	if err == nil || !strings.Contains(err.Error(), "serve not enabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestFindCLI(t *testing.T) {
	missing := errors.New("missing")
	ok := func(string) error { return nil }
	no := func(string) error { return missing }

	got, err := FindCLI(func(string) (string, error) { return "/usr/local/bin/tailscale", nil }, no)
	if err != nil || got != "/usr/local/bin/tailscale" {
		t.Fatalf("path: %q %v", got, err)
	}
	got, err = FindCLI(func(string) (string, error) { return "", missing }, ok)
	if err != nil || got != AppBundleCLI {
		t.Fatalf("bundle: %q %v", got, err)
	}
	_, err = FindCLI(func(string) (string, error) { return "", missing }, no)
	if err == nil || !strings.Contains(err.Error(), "install") {
		t.Fatalf("err = %v", err)
	}
}
