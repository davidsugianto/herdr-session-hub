package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture loads a SYNTHETIC screen (see testdata/README.md).
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func loadRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

func mustParse(t *testing.T, p Profile, screen string) Prompt {
	t.Helper()
	pr, ok := p.Parse(screen)
	if !ok {
		t.Fatalf("%s: prompt not recognised in:\n%s", p.Name(), screen)
	}
	return pr
}

func keysOf(t *testing.T, p Profile, a Action, pr Prompt) []string {
	t.Helper()
	k, ok := p.Keys(a, pr)
	if !ok {
		t.Fatalf("%s: no keys for %s", p.Name(), a)
	}
	return k
}

func TestLoadAndProviderContract(t *testing.T) {
	var _ Provider = (*Registry)(nil)
	r := loadRegistry(t)
	for _, n := range []string{"claude", "pi", "omp", "generic"} {
		p := r.For(n)
		if p.Name() != n {
			t.Errorf("For(%q).Name() = %q", n, p.Name())
		}
		if p.Verified() {
			t.Errorf("%s must be verified=false until real screens are captured", n)
		}
		if k, ok := p.Keys(ActionInterrupt, Prompt{}); !ok || !reflect.DeepEqual(k, []string{"esc"}) {
			t.Errorf("%s interrupt = %v, %v", n, k, ok)
		}
		if k, ok := p.Keys(ActionForceInterrupt, Prompt{}); !ok || !reflect.DeepEqual(k, []string{"ctrl+c"}) {
			t.Errorf("%s force_interrupt = %v, %v", n, k, ok)
		}
	}
}

func TestForCaseInsensitiveAndFallback(t *testing.T) {
	r := loadRegistry(t)
	for _, n := range []string{"Claude", "CLAUDE", " claude ", "claude-code"} {
		if got := r.For(n).Name(); got != "claude" {
			t.Errorf("For(%q) = %q, want claude", n, got)
		}
	}
	if got := r.For("OMP").Name(); got != "omp" {
		t.Errorf("For(OMP) = %q", got)
	}
	for _, n := range []string{"", "bash", "some-new-agent"} {
		p := r.For(n)
		if p == nil || p.Name() != "generic" {
			t.Errorf("For(%q) = %v, want generic", n, p)
		}
	}
}

func TestClaudeParse(t *testing.T) {
	p := loadRegistry(t).For("claude")
	pr := mustParse(t, p, fixture(t, "claude_bash.txt"))
	if !strings.Contains(pr.Text, "go test ./...") || !strings.HasSuffix(pr.Text, "Do you want to proceed?") {
		t.Errorf("Text = %q", pr.Text)
	}
	if strings.Contains(pr.Text, "●") {
		t.Errorf("Text leaked unrelated output above the prompt header: %q", pr.Text)
	}
	want := []string{
		"Yes",
		"Yes, and don't ask again for go test commands in /work",
		"No, and tell Claude what to do differently (esc)",
	}
	if !reflect.DeepEqual(pr.Options, want) {
		t.Errorf("Options = %q, want %q", pr.Options, want)
	}
	if pr.TrustRule != "Bash(go test:*)" {
		t.Errorf("TrustRule = %q", pr.TrustRule)
	}
	if len(pr.Hash) != 64 {
		t.Errorf("Hash = %q, want 64 hex chars", pr.Hash)
	}
}

func TestClaudeHashStabilityAndChange(t *testing.T) {
	p := loadRegistry(t).For("claude")
	a := mustParse(t, p, fixture(t, "claude_bash.txt"))
	again := mustParse(t, p, fixture(t, "claude_bash.txt"))
	redraw := mustParse(t, p, fixture(t, "claude_bash_redraw.txt")) // cursor moved, extra spaces
	other := mustParse(t, p, fixture(t, "claude_bash_other.txt"))   // different command
	if a.Hash != again.Hash {
		t.Error("hash not deterministic")
	}
	if a.Hash != redraw.Hash {
		t.Errorf("cosmetic redraw changed the hash:\n%q\n%q", a, redraw)
	}
	if a.Hash == other.Hash {
		t.Error("a different command must change the hash")
	}
}

func TestClaudeKeys(t *testing.T) {
	p := loadRegistry(t).For("claude")
	pr := mustParse(t, p, fixture(t, "claude_bash.txt"))
	cases := map[Action][]string{
		ActionApprove: {"1", "enter"},
		ActionTrust:   {"2", "enter"},
		ActionDeny:    {"3", "enter"},
	}
	for a, want := range cases {
		if got := keysOf(t, p, a, pr); !reflect.DeepEqual(got, want) {
			t.Errorf("%s keys = %v, want %v", a, got, want)
		}
	}
}

func TestClaudeKeysComputedFromScreen(t *testing.T) {
	p := loadRegistry(t).For("claude")
	// Only Yes / No: no trust option, and No is option 2.
	pr := mustParse(t, p, fixture(t, "claude_two_options.txt"))
	if got := keysOf(t, p, ActionApprove, pr); !reflect.DeepEqual(got, []string{"1", "enter"}) {
		t.Errorf("approve = %v", got)
	}
	if got := keysOf(t, p, ActionDeny, pr); !reflect.DeepEqual(got, []string{"2", "enter"}) {
		t.Errorf("deny = %v", got)
	}
	if k, ok := p.Keys(ActionTrust, pr); ok || k != nil {
		t.Errorf("trust without a trust option = %v, %v; want nil,false", k, ok)
	}
	if pr.TrustRule != "" {
		t.Errorf("TrustRule = %q, want empty", pr.TrustRule)
	}
}

func TestClaudeNoPrompt(t *testing.T) {
	p := loadRegistry(t).For("claude")
	if pr, ok := p.Parse(fixture(t, "claude_noprompt.txt")); ok {
		t.Errorf("recognised a prompt in a plain screen: %+v", pr)
	}
	if _, ok := p.Parse(""); ok {
		t.Error("recognised a prompt in an empty screen")
	}
	// A question with no options is not a prompt either.
	if _, ok := p.Parse("Do you want to proceed?\n"); ok {
		t.Error("question without options recognised")
	}
}

func TestClaudeANSI(t *testing.T) {
	p := loadRegistry(t).For("claude")
	raw := fixture(t, "claude_ansi.txt")
	if !strings.Contains(raw, "\x1b[") {
		t.Fatal("fixture lost its escape sequences")
	}
	pr := mustParse(t, p, raw)
	if strings.Contains(pr.Text, "\x1b") {
		t.Errorf("ANSI leaked into Text: %q", pr.Text)
	}
	for _, o := range pr.Options {
		if strings.Contains(o, "\x1b") || strings.Contains(o, "title") {
			t.Errorf("ANSI/OSC leaked into option %q", o)
		}
	}
	if got := keysOf(t, p, ActionTrust, pr); !reflect.DeepEqual(got, []string{"2", "enter"}) {
		t.Errorf("trust = %v", got)
	}
	// The same prompt, coloured or not, hashes the same.
	plain := strings.NewReplacer("\x1b[1m", "", "\x1b[36m", "", "\x1b[33m", "", "\x1b[0m", "", "\x1b[2K", "", "\x1b]0;title\x07", "").Replace(raw)
	if mustParse(t, p, plain).Hash != pr.Hash {
		t.Error("colour changed the hash")
	}
}

func TestOnlyTailCounts(t *testing.T) {
	p := loadRegistry(t).For("claude")
	prompt := fixture(t, "claude_two_options.txt")
	filler := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "output line %d\n", i)
		}
		return b.String()
	}
	// Plenty of earlier output above the prompt: still found.
	if _, ok := p.Parse(filler(200) + prompt); !ok {
		t.Error("prompt at the bottom of a long screen not found")
	}
	// Prompt buried under more than the tail window: ignored.
	if pr, ok := p.Parse(prompt + filler(60)); ok {
		t.Errorf("buried prompt recognised: %+v", pr)
	}
	// Trailing blank padding (viewport rows) does not push it out of the tail.
	if _, ok := p.Parse(prompt + strings.Repeat("\n", 80)); !ok {
		t.Error("blank padding below the prompt hid it")
	}
}

func TestLatestPromptWins(t *testing.T) {
	p := loadRegistry(t).For("claude")
	screen := fixture(t, "claude_bash_other.txt") + "\n" + fixture(t, "claude_bash.txt")
	got := mustParse(t, p, screen)
	want := mustParse(t, p, fixture(t, "claude_bash.txt"))
	if got.Hash != want.Hash {
		t.Error("an older prompt higher up the screen took priority over the latest one")
	}
}

func TestOMP(t *testing.T) {
	p := loadRegistry(t).For("omp")
	pr := mustParse(t, p, fixture(t, "omp_picker.txt"))
	if pr.Text != "Which file should I edit?" || len(pr.Options) != 3 {
		t.Fatalf("prompt = %+v", pr)
	}
	// "main.go" matches no approve regex, so approve is absent; trust and deny map.
	if k, ok := p.Keys(ActionApprove, pr); ok {
		t.Errorf("approve = %v, want none", k)
	}
	if got := keysOf(t, p, ActionTrust, pr); !reflect.DeepEqual(got, []string{"2", "enter"}) {
		t.Errorf("trust = %v", got)
	}
	if got := keysOf(t, p, ActionDeny, pr); !reflect.DeepEqual(got, []string{"3", "enter"}) {
		t.Errorf("deny = %v", got)
	}
}

func TestPiHasNoTrust(t *testing.T) {
	p := loadRegistry(t).For("pi")
	pr := mustParse(t, p, fixture(t, "pi_prompt.txt"))
	if got := keysOf(t, p, ActionApprove, pr); !reflect.DeepEqual(got, []string{"1", "enter"}) {
		t.Errorf("approve = %v", got)
	}
	if got := keysOf(t, p, ActionDeny, pr); !reflect.DeepEqual(got, []string{"3", "enter"}) {
		t.Errorf("deny = %v", got)
	}
	if k, ok := p.Keys(ActionTrust, pr); ok || k != nil {
		t.Errorf("pi trust = %v, %v; want nil,false", k, ok)
	}
}

func TestGenericNeverParses(t *testing.T) {
	g := loadRegistry(t).For("unknown-agent")
	for _, f := range []string{"claude_bash.txt", "omp_picker.txt", "pi_prompt.txt", "claude_noprompt.txt"} {
		if pr, ok := g.Parse(fixture(t, f)); ok {
			t.Errorf("generic parsed %s: %+v", f, pr)
		}
	}
	for _, a := range []Action{ActionApprove, ActionTrust, ActionDeny} {
		if k, ok := g.Keys(a, Prompt{Options: []string{"Yes", "No"}}); ok || k != nil {
			t.Errorf("generic %s = %v, %v; want nil,false", a, k, ok)
		}
	}
}

func TestKeysAbsentOption(t *testing.T) {
	p := loadRegistry(t).For("claude")
	// A prompt struct whose options lack a "No": deny has no keys.
	pr := Prompt{Options: []string{"Yes", "Maybe"}}
	if k, ok := p.Keys(ActionDeny, pr); ok || k != nil {
		t.Errorf("deny = %v, %v; want nil,false", k, ok)
	}
	if k, ok := p.Keys(Action("bogus"), pr); ok || k != nil {
		t.Errorf("bogus action = %v, %v; want nil,false", k, ok)
	}
}

func TestLetterMarkers(t *testing.T) {
	files := map[string][]byte{
		"generic.toml": []byte(genericTOML),
		"lettered.toml": []byte(`
name = "lettered"
match = ["let"]
[prompt]
question = '^Proceed\?$'
option = '^\(?([a-z])[.)]\s+(\S.*)$'
marker = "letter"
approve = '(?i)^yes'
deny = '(?i)^no'
[keys]
approve = ["{approve_option}"]
deny = ["{deny_option}", "enter"]
`),
	}
	r, err := NewRegistry(files)
	if err != nil {
		t.Fatal(err)
	}
	p := r.For("LET")
	pr := mustParse(t, p, "Proceed?\na) Yes\nb) No\n")
	if got := keysOf(t, p, ActionApprove, pr); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("approve = %v", got)
	}
	if got := keysOf(t, p, ActionDeny, pr); !reflect.DeepEqual(got, []string{"b", "enter"}) {
		t.Errorf("deny = %v", got)
	}
}

const genericTOML = `
name = "generic"
[keys]
interrupt = ["esc"]
`

func TestMalformedProfilesRejected(t *testing.T) {
	const okPrompt = `
[prompt]
question = '^Q\?$'
option = '^(\d+)\.\s+(.+)$'
marker = "number"
approve = '^Yes'
`
	cases := []struct {
		name string
		toml string
		want string // substring expected in the error besides the file name
	}{
		{"bad toml", `name = `, "parse toml"},
		{"missing name", `verified = true`, "name"},
		{"bad regex", "name = \"x\"\n[prompt]\nquestion = '('\noption = '^(\\d+)\\.\\s+(.+)$'\nmarker = \"number\"\napprove = 'y'\n", "prompt.question"},
		{"missing question", "name = \"x\"\n[prompt]\noption = '^(\\d+)\\.\\s+(.+)$'\nmarker = \"number\"\napprove = 'y'\n", "question"},
		{"missing option", "name = \"x\"\n[prompt]\nquestion = 'q'\nmarker = \"number\"\napprove = 'y'\n", "option"},
		{"one capture group", "name = \"x\"\n[prompt]\nquestion = 'q'\noption = '^(\\d+)\\. .*$'\nmarker = \"number\"\napprove = 'y'\n", "capture groups"},
		{"missing marker", "name = \"x\"\n[prompt]\nquestion = 'q'\noption = '^(\\d+)\\.\\s+(.+)$'\napprove = 'y'\n", "marker"},
		{"no roles", "name = \"x\"\n[prompt]\nquestion = 'q'\noption = '^(\\d+)\\.\\s+(.+)$'\nmarker = \"number\"\n", "approve, trust, deny"},
		{"unknown key", "name = \"x\"\nbogus = 1\n", "parse toml"},
		{"unknown action", "name = \"x\"\n[keys]\nlaunch = [\"enter\"]\n", "unknown action"},
		{"empty key list", "name = \"x\"\n[keys]\ninterrupt = []\n", "empty key list"},
		{"unknown placeholder", "name = \"x\"" + okPrompt + "[keys]\napprove = [\"{nope}\"]\n", "placeholder"},
		{"placeholder without role", "name = \"x\"" + okPrompt + "[keys]\ntrust = [\"{trust_option}\"]\n", "trust regex"},
		{"approve keys without prompt", "name = \"x\"\n[keys]\napprove = [\"enter\"]\n", "[prompt]"},
		{"trust_rule without template", "name = \"x\"\n[prompt]\nquestion = 'q'\noption = '^(\\d+)\\.\\s+(.+)$'\nmarker = \"number\"\ntrust = 'y'\ntrust_rule = 'z'\n", "trust_rule_template"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewRegistry(map[string][]byte{
				"generic.toml": []byte(genericTOML),
				"bad.toml":     []byte(c.toml),
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "bad.toml") {
				t.Errorf("error does not name the file: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestRegistryStructureErrors(t *testing.T) {
	g := []byte(genericTOML)
	x := func(name string, match ...string) []byte {
		m := make([]string, len(match))
		for i, s := range match {
			m[i] = fmt.Sprintf("%q", s)
		}
		return []byte(fmt.Sprintf("name = %q\nmatch = [%s]\n", name, strings.Join(m, ",")))
	}
	t.Run("duplicate name", func(t *testing.T) {
		_, err := NewRegistry(map[string][]byte{"generic.toml": g, "a.toml": x("dup"), "b.toml": x("DUP")})
		if err == nil || !strings.Contains(err.Error(), "b.toml") || !strings.Contains(err.Error(), "duplicate profile name") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("duplicate agent name across files", func(t *testing.T) {
		_, err := NewRegistry(map[string][]byte{"generic.toml": g, "a.toml": x("a", "shared"), "b.toml": x("b", "Shared")})
		if err == nil || !strings.Contains(err.Error(), "b.toml") || !strings.Contains(err.Error(), "shared") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("duplicate agent name within a file", func(t *testing.T) {
		_, err := NewRegistry(map[string][]byte{"generic.toml": g, "a.toml": x("a", "z", "Z")})
		if err == nil || !strings.Contains(err.Error(), "a.toml") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("missing generic", func(t *testing.T) {
		if _, err := NewRegistry(map[string][]byte{"a.toml": x("a")}); err == nil || !strings.Contains(err.Error(), "generic") {
			t.Errorf("err = %v", err)
		}
	})
}
