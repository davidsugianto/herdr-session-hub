package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

func testSub(t *testing.T, endpoint string) store.PushSub {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return store.PushSub{Endpoint: endpoint, DeviceID: "d1",
		P256dh: base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(auth)}
}

func TestWebPushSinkStatusMapping(t *testing.T) {
	var mu sync.Mutex
	var gotHeaders http.Header
	status := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		code := status
		mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	pub, priv, err := generateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	sink := newWebPushSink(pub, priv, "mailto:me@example.com", srv.Client())
	sub := testSub(t, srv.URL+"/push/abc")
	ctx := context.Background()

	cases := []struct {
		code    int
		kind    Kind
		gone    bool
		ok      bool
		urgency string
	}{
		{http.StatusCreated, KindBlocked, false, true, "high"},
		{http.StatusOK, KindFinished, false, true, "normal"},
		{http.StatusNotFound, KindBlocked, true, false, "high"},
		{http.StatusGone, KindBlocked, true, false, "high"},
		{http.StatusTooManyRequests, KindFinished, false, false, "normal"},
		{http.StatusInternalServerError, KindFinished, false, false, "normal"},
	}
	for _, tc := range cases {
		mu.Lock()
		status = tc.code
		mu.Unlock()
		err := sink.Send(ctx, sub, Payload{Title: "t", Kind: tc.kind})
		switch {
		case tc.ok && err != nil:
			t.Errorf("%d: err = %v", tc.code, err)
		case tc.gone && !errors.Is(err, ErrGone):
			t.Errorf("%d: err = %v, want ErrGone", tc.code, err)
		case !tc.ok && !tc.gone && (err == nil || errors.Is(err, ErrGone) || !strings.Contains(err.Error(), "nope")):
			t.Errorf("%d: err = %v, want a plain error with the body", tc.code, err)
		}
		mu.Lock()
		h := gotHeaders
		mu.Unlock()
		if h.Get("Urgency") != tc.urgency || h.Get("TTL") != "60" || h.Get("Content-Encoding") != "aes128gcm" {
			t.Errorf("%d: headers %v", tc.code, h)
		}
		if a := h.Get("Authorization"); !strings.HasPrefix(a, "vapid t=") || !strings.Contains(a, "k="+pub) {
			t.Errorf("%d: authorization %q", tc.code, a)
		}
	}
}

func TestSubscriberStripsMailto(t *testing.T) {
	if got := subscriber("mailto:hub@example.com"); got != "hub@example.com" {
		t.Fatalf("got %q", got)
	}
	if got := subscriber("https://example.com"); got != "https://example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestWebPushSinkDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	pub, priv, err := generateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	sink := newWebPushSink(pub, priv, "mailto:me@example.com", nil) // default client
	err = sink.Send(context.Background(), testSub(t, redirect.URL+"/push/abc"), Payload{Title: "t", Kind: KindBlocked})
	if err == nil || errors.Is(err, ErrGone) {
		t.Fatalf("err = %v, want a non-gone error", err)
	}
	if !strings.Contains(err.Error(), "307") {
		t.Fatalf("err = %v, want the 307 status", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("redirect target hit %d times", n)
	}
}
