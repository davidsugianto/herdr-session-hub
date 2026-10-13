package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

// Valid base64url (unpadded) keys: 65-byte P-256 point and 16-byte secret.
var (
	testP256dh = "B" + strings.Repeat("A", 86)
	testAuth   = strings.Repeat("A", 22)
)

func withPush(n *fakeNotifier) func(*Config) {
	return func(c *Config) {
		fs := c.Store.(*fakeStore)
		c.Push, c.Mutes = fs, fs
		if n != nil {
			c.Notifier = n
		}
	}
}

func subBody(endpoint string) string {
	return fmt.Sprintf(`{"subscription":{"endpoint":%q,"expirationTime":null,"keys":{"p256dh":%q,"auth":%q}}}`, endpoint, testP256dh, testAuth)
}

func TestPushKey(t *testing.T) {
	e := newEnv(t, withPush(&fakeNotifier{key: "BPUB"}))
	w := e.do("GET", "/api/push/key", "")
	if w.Code != 200 || decode[map[string]string](t, w)["public_key"] != "BPUB" {
		t.Fatalf("key = %d %s", w.Code, w.Body)
	}
	n := newEnv(t, withPush(nil))
	if w := n.do("GET", "/api/push/key", ""); w.Code != 503 || errCode(t, w) != "push_unavailable" {
		t.Fatalf("no notifier = %d %s", w.Code, w.Body)
	}
}

func TestPushSubscribe(t *testing.T) {
	e := newEnv(t, withPush(&fakeNotifier{}))
	w := e.do("POST", "/api/push/subscribe", subBody("https://fcm.googleapis.com/abc"))
	if w.Code != 200 || !decode[map[string]bool](t, w)["ok"] {
		t.Fatalf("subscribe = %d %s", w.Code, w.Body)
	}
	subs, _ := e.st.PushSubs(t.Context())
	if len(subs) != 1 || subs[0].DeviceID != "dev-1" || subs[0].Endpoint != "https://fcm.googleapis.com/abc" ||
		subs[0].P256dh != testP256dh || subs[0].Auth != testAuth || !subs[0].CreatedAt.Equal(e.clock.Now()) {
		t.Fatalf("subs = %+v", subs)
	}
	rows := e.st.auditRows()
	if len(rows) != 1 || rows[0].Action != "push_subscribe" || rows[0].Actor != "dev-1" || rows[0].Result != "ok" ||
		!strings.Contains(rows[0].Payload, "fcm.googleapis.com") {
		t.Fatalf("audit = %+v", rows)
	}
	if strings.Contains(rows[0].Payload, testAuth) {
		t.Fatalf("audit leaks key material: %s", rows[0].Payload)
	}
}

func TestPushSubscribeInvalid(t *testing.T) {
	e := newEnv(t, withPush(&fakeNotifier{}))
	long := "https://fcm.googleapis.com/" + strings.Repeat("a", 2048)
	bad := []string{
		``, `{}`, `nope`,
		subBody("http://fcm.googleapis.com/abc"),
		subBody("https:///nohost"),
		subBody("fcm.googleapis.com/abc"),
		subBody("https://user:pw@fcm.googleapis.com/abc"),
		subBody(long),
		`{"subscription":{"endpoint":"https://fcm.googleapis.com/a","keys":{"p256dh":"","auth":"` + testAuth + `"}}}`,
		`{"subscription":{"endpoint":"https://fcm.googleapis.com/a","keys":{"p256dh":"` + testP256dh + `","auth":"!!!"}}}`,
		`{"subscription":{"endpoint":"https://fcm.googleapis.com/a","keys":{"p256dh":"AAAA","auth":"` + testAuth + `"}}}`,
		`{"subscription":{"endpoint":"https://fcm.googleapis.com/a"}}`,
		`{"subscription":{"endpoint":"https://fcm.googleapis.com/a","keys":{"p256dh":"` + testP256dh + `","auth":"` + testAuth + `"}},"extra":1}`,
	}
	for _, b := range bad {
		if w := e.do("POST", "/api/push/subscribe", b); w.Code != 400 {
			t.Errorf("body %.80q = %d, want 400 (%s)", b, w.Code, w.Body)
		}
	}
	if subs, _ := e.st.PushSubs(t.Context()); len(subs) != 0 {
		t.Fatalf("stored invalid subs: %+v", subs)
	}
}

func TestPushUnsubscribeOwnOnly(t *testing.T) {
	e := newEnv(t, withPush(&fakeNotifier{}))
	_ = e.st.AddPushSub(t.Context(), store.PushSub{Endpoint: "https://p.example/mine", DeviceID: "dev-1"})
	_ = e.st.AddPushSub(t.Context(), store.PushSub{Endpoint: "https://p.example/theirs", DeviceID: "dev-2"})
	if w := e.do("DELETE", "/api/push/subscribe", `{"endpoint":"https://p.example/theirs"}`); w.Code != 200 {
		t.Fatalf("delete other = %d %s", w.Code, w.Body)
	}
	if w := e.do("DELETE", "/api/push/subscribe", `{"endpoint":"https://p.example/mine"}`); w.Code != 200 || !decode[map[string]bool](t, w)["ok"] {
		t.Fatalf("delete own = %d %s", w.Code, w.Body)
	}
	subs, _ := e.st.PushSubs(t.Context())
	if len(subs) != 1 || subs[0].Endpoint != "https://p.example/theirs" {
		t.Fatalf("subs = %+v", subs)
	}
	if w := e.do("DELETE", "/api/push/subscribe", `{"endpoint":""}`); w.Code != 400 {
		t.Fatalf("empty endpoint = %d", w.Code)
	}
	if rows := e.st.auditRows(); len(rows) != 3 || rows[0].Action != "push_unsubscribe" {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestPushTest(t *testing.T) {
	n := &fakeNotifier{}
	e := newEnv(t, withPush(n))
	if w := e.do("POST", "/api/push/test", ""); w.Code != 200 || !decode[map[string]bool](t, w)["ok"] {
		t.Fatalf("test = %d %s", w.Code, w.Body)
	}
	if got := n.testCalls(); len(got) != 1 || got[0] != "dev-1" {
		t.Fatalf("SendTest calls = %v", got)
	}
	n.mu.Lock()
	n.err = fmt.Errorf("wrap: %w", store.ErrNotFound)
	n.mu.Unlock()
	if w := e.do("POST", "/api/push/test", ""); w.Code != 409 || errCode(t, w) != "no_subscription" {
		t.Fatalf("no sub = %d %s", w.Code, w.Body)
	}
	n.mu.Lock()
	n.err = errors.New("push service 500")
	n.mu.Unlock()
	if w := e.do("POST", "/api/push/test", ""); w.Code != 502 || errCode(t, w) != "push_failed" {
		t.Fatalf("send failed = %d %s", w.Code, w.Body)
	}
	if rows := e.st.auditRows(); len(rows) != 3 || rows[0].Action != "push_test" || rows[2].Result == "ok" {
		t.Fatalf("audit = %+v", rows)
	}
	u := newEnv(t, withPush(nil))
	if w := u.do("POST", "/api/push/test", ""); w.Code != 503 || errCode(t, w) != "push_unavailable" {
		t.Fatalf("no notifier = %d %s", w.Code, w.Body)
	}
}

func TestMutes(t *testing.T) {
	e := newEnv(t, withPush(nil))
	now := e.clock.Now()
	until := now.Add(time.Hour).Format(time.RFC3339)
	if w := e.do("PUT", "/api/mutes", `{"scope":"agent","key":"labs/term_1","until":"`+until+`"}`); w.Code != 200 || !decode[map[string]bool](t, w)["ok"] {
		t.Fatalf("put = %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/mutes", `{"scope":"session","key":"labs","until":null}`); w.Code != 200 {
		t.Fatalf("put forever = %d %s", w.Code, w.Body)
	}
	w := e.do("GET", "/api/mutes", "")
	type row struct {
		Scope     string  `json:"scope"`
		Key       string  `json:"key"`
		Until     *string `json:"until"`
		CreatedAt string  `json:"created_at"`
	}
	got := decode[struct {
		Mutes []row `json:"mutes"`
	}](t, w)
	if w.Code != 200 || len(got.Mutes) != 2 {
		t.Fatalf("mutes = %d %s", w.Code, w.Body)
	}
	a, s := got.Mutes[0], got.Mutes[1]
	if a.Scope != "agent" || a.Key != "labs/term_1" || a.Until == nil || *a.Until != until || a.CreatedAt != now.Format(time.RFC3339) {
		t.Fatalf("agent mute = %+v", a)
	}
	if s.Scope != "session" || s.Until != nil || !strings.Contains(w.Body.String(), `"until":null`) {
		t.Fatalf("session mute = %+v (%s)", s, w.Body)
	}
	// Expired mutes are not listed.
	e.clock.Advance(2 * time.Hour)
	if got := decode[struct{ Mutes []row }](t, e.do("GET", "/api/mutes", "")); len(got.Mutes) != 1 {
		t.Fatalf("after expiry = %+v", got)
	}
	if w := e.do("DELETE", "/api/mutes", `{"scope":"session","key":"labs"}`); w.Code != 200 {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/mutes", ""); strings.TrimSpace(w.Body.String()) != `{"mutes":[]}` {
		t.Fatalf("empty list = %s", w.Body)
	}
	rows := e.st.auditRows()
	if len(rows) != 3 || rows[0].Action != "mute_set" || rows[2].Action != "mute_delete" || rows[0].AgentID != "labs/term_1" {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestMutesInvalid(t *testing.T) {
	e := newEnv(t, withPush(nil))
	past := e.clock.Now().Add(-time.Minute).Format(time.RFC3339)
	for _, b := range []string{
		``, `{}`,
		`{"scope":"global","key":"x","until":null}`,
		`{"scope":"agent","key":"","until":null}`,
		`{"scope":"agent","key":"   ","until":null}`,
		`{"scope":"agent","key":"x","until":"` + past + `"}`,
		`{"scope":"agent","key":"x","until":"tomorrow"}`,
		`{"scope":"agent","key":"` + strings.Repeat("k", 300) + `","until":null}`,
	} {
		if w := e.do("PUT", "/api/mutes", b); w.Code != 400 {
			t.Errorf("put %.60q = %d, want 400 (%s)", b, w.Code, w.Body)
		}
	}
	for _, b := range []string{``, `{"scope":"x","key":"y"}`, `{"scope":"agent","key":""}`} {
		if w := e.do("DELETE", "/api/mutes", b); w.Code != 400 {
			t.Errorf("delete %q = %d, want 400", b, w.Code)
		}
	}
}

func TestMutesUnavailable(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ m, b string }{{"GET", ""}, {"PUT", `{"scope":"agent","key":"x","until":null}`}, {"DELETE", `{"scope":"agent","key":"x"}`}} {
		if w := e.do(c.m, "/api/mutes", c.b); w.Code != 503 || errCode(t, w) != "not_configured" {
			t.Errorf("%s = %d %s", c.m, w.Code, w.Body)
		}
	}
	for _, c := range []struct{ m, b string }{{"POST", subBody("https://fcm.googleapis.com/fcm/send/a")}, {"DELETE", `{"endpoint":"https://p.example/a"}`}} {
		if w := e.do(c.m, "/api/push/subscribe", c.b); w.Code != 503 || errCode(t, w) != "push_unavailable" {
			t.Errorf("subscribe %s = %d %s", c.m, w.Code, w.Body)
		}
	}
}

func TestNotifySettings(t *testing.T) {
	e := newEnv(t, withPush(&fakeNotifier{}), func(c *Config) {
		c.Quiet = []QuietRule{{Session: "labs", Rule: "22:00-07:00"}}
		c.BlockedBreaksQuiet = true
	})
	w := e.do("GET", "/api/notify", "")
	want := `{"quiet_hours":[{"session":"labs","rule":"22:00-07:00"}],"blocked_breaks_quiet":true,"push_available":true}`
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("notify = %d %s", w.Code, w.Body)
	}
	d := newEnv(t)
	want = `{"quiet_hours":[],"blocked_breaks_quiet":false,"push_available":false}`
	if w := d.do("GET", "/api/notify", ""); strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("default notify = %s", w.Body)
	}
}

func TestPushRoutesAuthCSRFAndRateLimit(t *testing.T) {
	e := newEnv(t, withPush(&fakeNotifier{key: "k"}))
	routes := []struct{ m, p, b string }{
		{"GET", "/api/push/key", ""}, {"POST", "/api/push/subscribe", subBody("https://fcm.googleapis.com/fcm/send/a")},
		{"DELETE", "/api/push/subscribe", `{"endpoint":"https://p.example/a"}`}, {"POST", "/api/push/test", ""},
		{"GET", "/api/mutes", ""}, {"PUT", "/api/mutes", `{"scope":"agent","key":"x","until":null}`},
		{"DELETE", "/api/mutes", `{"scope":"agent","key":"x"}`}, {"GET", "/api/notify", ""},
	}
	for _, r := range routes {
		if w := e.serve(req(r.m, r.p, r.b)); w.Code != 401 {
			t.Errorf("anon %s %s = %d", r.m, r.p, w.Code)
		}
		w := e.serve(req(r.m, r.p, r.b, "Cookie", "hub_token=tok-1"))
		if r.m == "GET" {
			if w.Code != 200 {
				t.Errorf("cookie GET %s = %d", r.p, w.Code)
			}
			continue
		}
		if w.Code != 403 || errCode(t, w) != "csrf" {
			t.Errorf("cookie %s %s without csrf = %d %s", r.m, r.p, w.Code, w.Body)
		}
		if w := e.serve(req(r.m, r.p, r.b, "Cookie", "hub_token=tok-1", "X-Requested-With", "hub")); w.Code != 200 {
			t.Errorf("cookie %s %s with csrf = %d %s", r.m, r.p, w.Code, w.Body)
		}
	}
	// The action limiter (burst 30) covers the mutating routes.
	limited := false
	for i := 0; i < 40; i++ {
		if w := e.do("PUT", "/api/mutes", `{"scope":"agent","key":"x","until":null}`); w.Code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("PUT /api/mutes never rate limited")
	}
	if w := e.do("POST", "/api/push/test", ""); w.Code != 429 {
		t.Fatalf("push test shares limiter = %d", w.Code)
	}
}

func TestPushSubscribeEndpointAllowList(t *testing.T) {
	e := newEnv(t, withPush(&fakeNotifier{}))
	for _, ep := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://FCM.googleapis.com/fcm/send/abc",
		"https://fcm.googleapis.com:443/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/abc",
		"https://api.push.apple.com/abc",
		"https://wns2-par02p.notify.windows.com/w/?token=abc",
		"https://other.push.services.mozilla.com/abc",
	} {
		if w := e.do("POST", "/api/push/subscribe", subBody(ep)); w.Code != 200 {
			t.Errorf("endpoint %q = %d, want 200 (%s)", ep, w.Code, w.Body)
		}
	}
	for _, ep := range []string{
		"https://evil.example/x",
		"https://fcm.googleapis.com.evil.example/x",
		"https://127.0.0.1/x",
		"https://[::1]/x",
		"https://localhost/x",
		"https://fcm.googleapis.com:8443/x",
		"https://fcm.googleapis.com:/x",
		"https://notpush.apple.com.evil.test/x",
		"https://evilpush.apple.com/x",
		"https://push.apple.com/x",
		"https://notify.windows.com/x",
		"https://user@fcm.googleapis.com/x",
		"http://fcm.googleapis.com/x",
	} {
		if w := e.do("POST", "/api/push/subscribe", subBody(ep)); w.Code != 400 {
			t.Errorf("endpoint %q = %d, want 400 (%s)", ep, w.Code, w.Body)
		}
	}
	subs, _ := e.st.PushSubs(t.Context())
	if len(subs) != 8 {
		t.Fatalf("stored %d subs, want 8", len(subs))
	}
}
