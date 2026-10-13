package config

import (
	"strings"
	"testing"
	"time"
)

// 2026-01-05 is a Monday.
func at(day int, hm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", "2026-01-"+twoDigits(4+day)+" "+hm, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func twoDigits(n int) string {
	return string([]byte{byte('0' + n/10), byte('0' + n%10)})
}

// day 1 = Monday ... 7 = Sunday.
func TestQuietRuleActive(t *testing.T) {
	cases := []struct {
		rule string
		t    time.Time
		want bool
	}{
		{"Sat-Sun", at(6, "12:00"), true},
		{"Sat-Sun", at(7, "00:00"), true},
		{"Sat-Sun", at(5, "23:59"), false},
		{"sat-sun", at(6, "01:00"), true}, // case-insensitive
		{"Mon", at(1, "09:00"), true},
		{"Mon", at(2, "09:00"), false},
		{"Sat-Mon", at(6, "10:00"), true}, // wraps the week
		{"Sat-Mon", at(7, "10:00"), true},
		{"Sat-Mon", at(1, "10:00"), true},
		{"Sat-Mon", at(2, "10:00"), false},
		{"Sat-Mon", at(5, "10:00"), false},
		{"22:00-07:00", at(1, "23:00"), true},
		{"22:00-07:00", at(1, "03:00"), true},
		{"22:00-07:00", at(1, "07:00"), false}, // end exclusive
		{"22:00-07:00", at(1, "22:00"), true},  // start inclusive
		{"22:00-07:00", at(1, "12:00"), false},
		{"09:00-17:00", at(3, "09:00"), true},
		{"09:00-17:00", at(3, "16:59"), true},
		{"09:00-17:00", at(3, "17:00"), false},
		{"09:00-17:00", at(3, "08:59"), false},
		{"Sat-Sun, 22:00-07:00", at(3, "23:30"), true},
		{"Sat-Sun, 22:00-07:00", at(6, "12:00"), true},
		{"Sat-Sun, 22:00-07:00", at(3, "12:00"), false},
		{"", at(3, "12:00"), false},
	}
	for _, c := range cases {
		r, err := ParseQuietHours(c.rule)
		if err != nil {
			t.Fatalf("%q: %v", c.rule, err)
		}
		if got := r.Active(c.t); got != c.want {
			t.Errorf("%q at %s = %v, want %v", c.rule, c.t.Format("Mon 15:04"), got, c.want)
		}
	}
}

func TestQuietRuleUsesLocalTimeOfT(t *testing.T) {
	r, _ := ParseQuietHours("22:00-07:00")
	loc := time.FixedZone("x", 9*3600)
	// 14:00 UTC is 23:00 in loc.
	if !r.Active(time.Date(2026, 1, 5, 14, 0, 0, 0, time.UTC).In(loc)) {
		t.Error("23:00 local should be active")
	}
	if r.Active(time.Date(2026, 1, 5, 14, 0, 0, 0, time.UTC)) {
		t.Error("14:00 should not be active")
	}
}

func TestParseQuietHoursErrors(t *testing.T) {
	for _, bad := range []string{"Foo", "25:00-26:00", "22:00", "22:00-07", "Mon-", "Mon-Fri-Sun", "10:60-11:00", "10:00-10:00", "Mon,,Tue", "Mon, "} {
		if _, err := ParseQuietHours(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestConfigQuietActive(t *testing.T) {
	cfg := Config{Sessions: []Session{
		{Name: "A", QuietHours: "Sat-Sun"},
		{Name: "B"},
		{Name: "C", QuietHours: "garbage"},
	}}
	if !cfg.QuietActive("A", at(6, "12:00")) {
		t.Error("A on Saturday")
	}
	if cfg.QuietActive("A", at(2, "12:00")) {
		t.Error("A on Tuesday")
	}
	if cfg.QuietActive("B", at(6, "12:00")) || cfg.QuietActive("nope", at(6, "12:00")) {
		t.Error("no rule -> false")
	}
	if cfg.QuietActive("C", at(6, "12:00")) {
		t.Error("invalid rule -> false")
	}
}

func TestLoadQuietHoursValidation(t *testing.T) {
	p := writeFile(t, "[[session]]\nname = \"CEKAT\"\nquiet_hours = \"nonsense\"\n")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "CEKAT") {
		t.Fatalf("want error naming path and session, got %v", err)
	}
}

func TestLoadNotifyMacDefaults(t *testing.T) {
	for _, content := range []string{"", "# nothing\n"} {
		cfg, err := Load(writeFile(t, content))
		if err != nil {
			t.Fatal(err)
		}
		assertDefaults(t, cfg)
	}
	cfg, _ := Load(t.TempDir() + "/missing.toml")
	assertDefaults(t, cfg)
}

func assertDefaults(t *testing.T, cfg Config) {
	t.Helper()
	if !cfg.Notify.BlockedBreaksQuiet || cfg.Notify.Contact != "mailto:hub@example.com" || cfg.Mac.KeepAwake != "while-working" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
}

func TestLoadNotifyMacParsed(t *testing.T) {
	cfg, err := Load(writeFile(t, `
[notify]
blocked_breaks_quiet = false
contact = "mailto:me@example.org"
[mac]
keep_awake = "always"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notify.BlockedBreaksQuiet || cfg.Notify.Contact != "mailto:me@example.org" || cfg.Mac.KeepAwake != "always" {
		t.Fatalf("%+v", cfg)
	}
	// A partial [notify] keeps the other defaults.
	cfg, err = Load(writeFile(t, "[notify]\ncontact = \"mailto:a@b.c\"\n"))
	if err != nil || !cfg.Notify.BlockedBreaksQuiet {
		t.Fatalf("%v %+v", err, cfg)
	}
}

func TestLoadKeepAwakeInvalid(t *testing.T) {
	p := writeFile(t, "[mac]\nkeep_awake = \"sometimes\"\n")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("want error naming path, got %v", err)
	}
	// Old boolean form is not a valid mode either.
	if _, err := Load(writeFile(t, "[mac]\nkeep_awake = true\n")); err == nil {
		t.Fatal("bool should be rejected")
	}
	for _, m := range []string{"off", "while-working", "always"} {
		if _, err := Load(writeFile(t, "[mac]\nkeep_awake = \""+m+"\"\n")); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
}
