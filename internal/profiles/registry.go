package profiles

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed data/*.toml
var dataFS embed.FS

// genericName is the profile unknown agents fall back to.
const genericName = "generic"

const (
	defaultTailLines    = 40
	defaultContextLines = 12
	// maxOptionGap is how many non-blank lines may sit between the question
	// and its first option.
	maxOptionGap = 3
	minOptions   = 2
)

var _ Provider = (*Registry)(nil)

// Registry holds the loaded profiles and implements Provider.
type Registry struct {
	byAgent map[string]*profile
	generic *profile
}

// Load builds the registry from the embedded data files.
func Load() (*Registry, error) {
	entries, err := dataFS.ReadDir("data")
	if err != nil {
		return nil, fmt.Errorf("profiles: read embedded data: %w", err)
	}
	files := make(map[string][]byte, len(entries))
	for _, e := range entries {
		b, err := dataFS.ReadFile("data/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("profiles: read %s: %w", e.Name(), err)
		}
		files[e.Name()] = b
	}
	return NewRegistry(files)
}

// NewRegistry builds a registry from TOML files keyed by file name. Each file
// is validated; an error names the offending file. A profile named "generic"
// must be present.
func NewRegistry(files map[string][]byte) (*Registry, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	r := &Registry{byAgent: map[string]*profile{}}
	owner := map[string]string{} // lowercase agent name -> file
	profileFile := map[string]string{}
	for _, fn := range names {
		p, err := parseProfile(files[fn])
		if err != nil {
			return nil, fmt.Errorf("profiles: %s: %w", fn, err)
		}
		if prev, dup := profileFile[p.name]; dup {
			return nil, fmt.Errorf("profiles: %s: duplicate profile name %q (also in %s)", fn, p.name, prev)
		}
		profileFile[p.name] = fn
		for _, m := range p.match {
			if prev, dup := owner[m]; dup {
				return nil, fmt.Errorf("profiles: %s: agent name %q already matched by %s", fn, m, prev)
			}
			owner[m] = fn
			r.byAgent[m] = p
		}
		if p.name == genericName {
			r.generic = p
		}
	}
	if r.generic == nil {
		return nil, errors.New("profiles: no profile named \"generic\"")
	}
	return r, nil
}

// For returns the profile for an agent name (case-insensitive). Unknown agents
// get the generic profile; the result is never nil.
func (r *Registry) For(agent string) Profile {
	if p, ok := r.byAgent[strings.ToLower(strings.TrimSpace(agent))]; ok {
		return p
	}
	return r.generic
}

// --- TOML schema ---

type fileSchema struct {
	Name     string              `toml:"name"`
	Verified bool                `toml:"verified"`
	Match    []string            `toml:"match"`
	Prompt   *promptSchema       `toml:"prompt"`
	Keys     map[string][]string `toml:"keys"`
}

type promptSchema struct {
	TailLines         int    `toml:"tail_lines"`
	ContextLines      int    `toml:"context_lines"`
	ContextStart      string `toml:"context_start"`
	Question          string `toml:"question"`
	Option            string `toml:"option"`
	Marker            string `toml:"marker"`
	Approve           string `toml:"approve"`
	Trust             string `toml:"trust"`
	Deny              string `toml:"deny"`
	TrustRule         string `toml:"trust_rule"`
	TrustRuleTemplate string `toml:"trust_rule_template"`
}

// --- compiled profile ---

type promptSpec struct {
	tailLines, contextLines int
	contextStart            *regexp.Regexp
	question, option        *regexp.Regexp
	letters                 bool
	roles                   map[Action]*regexp.Regexp
	trustRule               *regexp.Regexp
	trustRuleTemplate       string
}

type profile struct {
	name     string
	verified bool
	match    []string // lowercase
	prompt   *promptSpec
	keys     map[Action][]string
}

var (
	knownActions = map[Action]bool{
		ActionApprove: true, ActionTrust: true, ActionDeny: true,
		ActionInterrupt: true, ActionForceInterrupt: true,
	}
	optionPlaceholders = map[string]Action{
		"{approve_option}": ActionApprove,
		"{trust_option}":   ActionTrust,
		"{deny_option}":    ActionDeny,
	}
)

func compile(field, expr string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%s: bad regex: %w", field, err)
	}
	return re, nil
}

func parseProfile(data []byte) (*profile, error) {
	var s fileSchema
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse toml: %w", err)
	}
	name := strings.ToLower(strings.TrimSpace(s.Name))
	if name == "" {
		return nil, errors.New("missing key: name")
	}
	p := &profile{name: name, verified: s.Verified, keys: map[Action][]string{}}

	seen := map[string]bool{}
	for _, m := range append([]string{name}, s.Match...) {
		m = strings.ToLower(strings.TrimSpace(m))
		if m == "" {
			return nil, errors.New("match: empty agent name")
		}
		if seen[m] {
			if m == name {
				continue
			}
			return nil, fmt.Errorf("match: duplicate agent name %q", m)
		}
		seen[m] = true
		p.match = append(p.match, m)
	}

	if s.Prompt != nil {
		spec, err := compilePrompt(s.Prompt)
		if err != nil {
			return nil, err
		}
		p.prompt = spec
	}

	for k, keys := range s.Keys {
		a := Action(k)
		if !knownActions[a] {
			return nil, fmt.Errorf("keys: unknown action %q", k)
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("keys.%s: empty key list", k)
		}
		for _, key := range keys {
			if strings.TrimSpace(key) == "" {
				return nil, fmt.Errorf("keys.%s: empty key", k)
			}
			if strings.ContainsAny(key, "{}") {
				role, ok := optionPlaceholders[key]
				if !ok {
					return nil, fmt.Errorf("keys.%s: unknown placeholder %q", k, key)
				}
				if p.prompt == nil || p.prompt.roles[role] == nil {
					return nil, fmt.Errorf("keys.%s: %s needs a [prompt] %s regex", k, key, role)
				}
			}
		}
		if (a == ActionApprove || a == ActionTrust || a == ActionDeny) && p.prompt == nil {
			return nil, fmt.Errorf("keys.%s: needs a [prompt] section", k)
		}
		p.keys[a] = append([]string(nil), keys...)
	}
	return p, nil
}

func compilePrompt(s *promptSchema) (*promptSpec, error) {
	spec := &promptSpec{
		tailLines:         s.TailLines,
		contextLines:      s.ContextLines,
		roles:             map[Action]*regexp.Regexp{},
		trustRuleTemplate: s.TrustRuleTemplate,
	}
	if spec.tailLines < 0 || spec.contextLines < 0 {
		return nil, errors.New("prompt: tail_lines and context_lines must not be negative")
	}
	if spec.tailLines == 0 {
		spec.tailLines = defaultTailLines
	}
	if spec.contextLines == 0 {
		spec.contextLines = defaultContextLines
	}
	if s.Question == "" {
		return nil, errors.New("prompt: missing key: question")
	}
	if s.Option == "" {
		return nil, errors.New("prompt: missing key: option")
	}
	var err error
	if spec.question, err = compile("prompt.question", s.Question); err != nil {
		return nil, err
	}
	if spec.option, err = compile("prompt.option", s.Option); err != nil {
		return nil, err
	}
	if spec.option.NumSubexp() < 2 {
		return nil, errors.New("prompt.option: needs two capture groups (marker, label)")
	}
	switch s.Marker {
	case "number":
	case "letter":
		spec.letters = true
	case "":
		return nil, errors.New("prompt: missing key: marker")
	default:
		return nil, fmt.Errorf("prompt.marker: %q must be \"number\" or \"letter\"", s.Marker)
	}
	if s.ContextStart != "" {
		if spec.contextStart, err = compile("prompt.context_start", s.ContextStart); err != nil {
			return nil, err
		}
	}
	for _, r := range []struct {
		a    Action
		expr string
	}{{ActionApprove, s.Approve}, {ActionTrust, s.Trust}, {ActionDeny, s.Deny}} {
		if r.expr == "" {
			continue
		}
		if spec.roles[r.a], err = compile("prompt."+string(r.a), r.expr); err != nil {
			return nil, err
		}
	}
	if len(spec.roles) == 0 {
		return nil, errors.New("prompt: at least one of approve, trust, deny is required")
	}
	if s.TrustRule != "" {
		if spec.trustRule, err = compile("prompt.trust_rule", s.TrustRule); err != nil {
			return nil, err
		}
		if s.TrustRuleTemplate == "" {
			return nil, errors.New("prompt: trust_rule needs trust_rule_template")
		}
		if spec.roles[ActionTrust] == nil {
			return nil, errors.New("prompt: trust_rule needs a trust regex")
		}
	}
	return spec, nil
}

// --- Profile implementation ---

func (p *profile) Name() string   { return p.name }
func (p *profile) Verified() bool { return p.verified }

var (
	ansiCSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiESC = regexp.MustCompile(`\x1b[@-Z\\-_]|\x1b[()][0-9A-Za-z]`)
	// boxOnly matches lines made only of whitespace and box-drawing glyphs.
	boxOnly = regexp.MustCompile(`^[\s\x{2500}-\x{257F}|]*$`)
)

func stripANSI(s string) string {
	s = ansiOSC.ReplaceAllString(s, "")
	s = ansiCSI.ReplaceAllString(s, "")
	return ansiESC.ReplaceAllString(s, "")
}

// cleanLines strips ANSI, drops trailing blank lines, keeps the last n lines
// and trims whitespace and side borders from each.
func cleanLines(screen string, n int) []string {
	screen = strings.ReplaceAll(stripANSI(screen), "\r", "")
	lines := strings.Split(screen, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	lines = lines[:end]
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.Trim(l, " \t │┃")
	}
	return out
}

func (p *profile) Parse(screen string) (Prompt, bool) {
	spec := p.prompt
	if spec == nil {
		return Prompt{}, false
	}
	lines := cleanLines(screen, spec.tailLines)
	for q := len(lines) - 1; q >= 0; q-- {
		if !spec.question.MatchString(lines[q]) || spec.option.MatchString(lines[q]) {
			continue
		}
		opts := spec.collectOptions(lines[q+1:])
		if len(opts) < minOptions {
			continue
		}
		text := strings.Join(append(spec.contextBefore(lines[:q]), lines[q]), "\n")
		pr := Prompt{Text: text, Options: opts, Hash: hashPrompt(text, opts)}
		if spec.trustRule != nil {
			if i := spec.roleIndex(ActionTrust, opts); i >= 0 {
				if m := spec.trustRule.FindStringSubmatchIndex(opts[i]); m != nil {
					pr.TrustRule = string(spec.trustRule.ExpandString(nil, spec.trustRuleTemplate, opts[i], m))
				}
			}
		}
		return pr, true
	}
	return Prompt{}, false
}

// marker returns the key for option index i, or "" if it cannot be typed.
func (s *promptSpec) marker(i int) string {
	if s.letters {
		if i >= 26 {
			return ""
		}
		return string(rune('a' + i))
	}
	if i >= 9 {
		return ""
	}
	return strconv.Itoa(i + 1)
}

// collectOptions reads the option run that starts within maxOptionGap lines of
// the question: markers must be in sequence from the first marker, and a blank
// line ends the run.
func (s *promptSpec) collectOptions(lines []string) []string {
	var opts []string
	gap := 0
	for _, l := range lines {
		if l == "" || boxOnly.MatchString(l) {
			if len(opts) > 0 {
				break
			}
			continue
		}
		m := s.option.FindStringSubmatch(l)
		if m != nil && strings.EqualFold(m[1], s.marker(len(opts))) {
			opts = append(opts, strings.TrimSpace(m[2]))
			continue
		}
		if len(opts) == 0 {
			if gap++; gap >= maxOptionGap {
				break
			}
		}
		// Otherwise a wrapped continuation or footer line: skip it.
	}
	return opts
}

// contextBefore returns the non-blank lines above the question that describe
// what is being approved, oldest first.
func (s *promptSpec) contextBefore(lines []string) []string {
	var ctx []string
	for i := len(lines) - 1; i >= 0 && len(ctx) < s.contextLines; i-- {
		l := lines[i]
		if l == "" || boxOnly.MatchString(l) {
			continue
		}
		if s.option.MatchString(l) {
			break // an earlier prompt
		}
		ctx = append(ctx, l)
		if s.contextStart != nil && s.contextStart.MatchString(l) {
			break
		}
	}
	for i, j := 0, len(ctx)-1; i < j; i, j = i+1, j-1 {
		ctx[i], ctx[j] = ctx[j], ctx[i]
	}
	return ctx
}

// roleIndex finds the option index serving an action, or -1. Trust and deny
// are resolved first; approve is the first match that is neither.
func (s *promptSpec) roleIndex(a Action, opts []string) int {
	find := func(role Action, skip ...int) int {
		re := s.roles[role]
		if re == nil {
			return -1
		}
	next:
		for i, o := range opts {
			for _, sk := range skip {
				if i == sk {
					continue next
				}
			}
			if re.MatchString(o) {
				return i
			}
		}
		return -1
	}
	trust := find(ActionTrust)
	if a == ActionTrust {
		return trust
	}
	deny := find(ActionDeny, trust)
	if a == ActionDeny {
		return deny
	}
	return find(ActionApprove, trust, deny)
}

func (p *profile) Keys(a Action, pr Prompt) ([]string, bool) {
	tmpl, ok := p.keys[a]
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(tmpl))
	for _, k := range tmpl {
		role, isPlaceholder := optionPlaceholders[k]
		if !isPlaceholder {
			out = append(out, k)
			continue
		}
		if p.prompt == nil {
			return nil, false
		}
		i := p.prompt.roleIndex(role, pr.Options)
		if i < 0 {
			return nil, false
		}
		m := p.prompt.marker(i)
		if m == "" {
			return nil, false
		}
		out = append(out, m)
	}
	return out, true
}

// hashPrompt hashes the normalised question text and option labels so that
// cosmetic redraws (cursor glyph, spacing, colours) keep the hash while a
// different command changes it.
func hashPrompt(text string, opts []string) string {
	h := sha256.New()
	h.Write([]byte(normalise(text)))
	for _, o := range opts {
		h.Write([]byte{0})
		h.Write([]byte(normalise(o)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normalise(s string) string {
	return strings.Join(strings.Fields(stripANSI(s)), " ")
}
