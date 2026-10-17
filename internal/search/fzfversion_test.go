package search

import (
	"reflect"
	"strings"
	"testing"

	"github.com/styd/magenta/internal/config"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"0.54.3 (brew)", Version{0, 54, 3}, true},
		{"0.54.3 (devel)", Version{0, 54, 3}, true},
		{"0.9.4", Version{0, 9, 4}, true},
		{"0.74.4 (Homebrew)\n", Version{0, 74, 4}, true},
		{"0.18", Version{0, 18, 0}, true},
		{"v0.30.0", Version{0, 30, 0}, true},
		{"", Version{}, false},
		{"garbage", Version{}, false},
		{"fzf version unknown", Version{}, false},
		{"1.x.3", Version{}, false},
		{"0.1.2.3", Version{}, false},
		{"-1.2.3", Version{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseVersion(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseVersion(%q) = %v, %v, want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestVersionLess(t *testing.T) {
	for _, tt := range []struct {
		a, b Version
		want bool
	}{
		{Version{0, 17, 9}, Version{0, 18, 0}, true},
		{Version{0, 18, 0}, Version{0, 18, 0}, false},
		{Version{0, 18, 1}, Version{0, 18, 0}, false},
		{Version{0, 54, 3}, Version{1, 0, 0}, true},
		{Version{1, 0, 0}, Version{0, 99, 99}, false},
	} {
		if got := tt.a.Less(tt.b); got != tt.want {
			t.Errorf("%v < %v = %v", tt.a, tt.b, got)
		}
	}
}

// The exact fzf syntax of the preview bindings, written out so that a change to it is deliberate.
// {HELPER} stands for the command that reaches the preview server. BuildArgs fills it in.
func hb(key, name string) string { return key + ":transform:{HELPER} --key " + name }

var (
	escBind   = hb("esc", "esc")
	enterBind = hb("enter", "enter")
	acceptBnd = "ctrl-o:accept"
	toggleOld = "ctrl-/:toggle-preview"
	toggleNew = hb("ctrl-/", "toggle")
)

const header = "enter: context   esc: back   tab: mark"

// navBinds are the keys that scroll an open preview and move the list when it is closed.
var navBinds = []string{escBind,
	hb("up", "up"), hb("down", "down"), hb("ctrl-p", "up"), hb("ctrl-n", "down"),
	hb("ctrl-k", "up"), hb("ctrl-j", "down"), hb("page-up", "page-up"), hb("page-down", "page-down"),
	hb("preview-scroll-up", "wheel-up"), hb("preview-scroll-down", "wheel-down")}

// previewBinds is navBinds followed by whatever else is given.
func previewBinds(rest ...string) []string {
	return append(append([]string(nil), navBinds...), rest...)
}

func TestPlanThresholds(t *testing.T) {
	def := config.Default()
	const (
		fullWin    = "up,50%,wrap,border-bottom,hidden"
		noHidden   = "up,50%,wrap,border-bottom"
		bare       = "up,50%,wrap"
		borderMsg  = "preview border (needs >= 0.27.0)"
		hiddenMsg  = "open the preview with Enter and close it with Esc (needs >= 0.49.0)"
		toggleMsg  = "cannot bind ctrl-/ (needs >= 0.19.0)"
		previewMsg = "too old for the preview pane (needs >= 0.18.0)"
	)
	full := previewBinds(enterBind, acceptBnd, toggleNew)
	tests := []struct {
		name        string
		v           Version
		layout      string
		wantPreview bool
		wantWindow  string
		wantBinds   []string
		wantHeader  string
		notices     []string // substrings, one per expected notice, in order
	}{
		{"below --layout", Version{0, 17, 3}, "", false, "", nil, "", []string{previewMsg}},
		{"at --layout, still no env vars", Version{0, 17, 4}, "default", false, "", nil, "", []string{previewMsg}},
		{"just below the env vars", Version{0, 17, 9}, "default", false, "", nil, "", []string{previewMsg}},
		{"at the env vars: no border, no Enter/Esc, no key chord", Version{0, 18, 0}, "default", true, bare, nil, "",
			[]string{borderMsg, hiddenMsg, toggleMsg}},
		{"just below key chords", Version{0, 18, 9}, "default", true, bare, nil, "", []string{borderMsg, hiddenMsg, toggleMsg}},
		{"at key chords", Version{0, 19, 0}, "default", true, bare, []string{toggleOld}, "", []string{borderMsg, hiddenMsg}},
		{"just below border", Version{0, 26, 9}, "default", true, bare, []string{toggleOld}, "", []string{borderMsg, hiddenMsg}},
		{"at border: preview always shown", Version{0, 27, 0}, "default", true, noHidden, []string{toggleOld}, "", []string{hiddenMsg}},
		{"just below the preview state", Version{0, 48, 9}, "default", true, noHidden, []string{toggleOld}, "", []string{hiddenMsg}},
		{"at the preview state: everything", Version{0, 49, 0}, "default", true, fullWin, full, header, nil},
		{"recent", Version{0, 74, 4}, "default", true, fullWin, full, header, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := PlanFor(tt.v, true, def)
			if p.Layout != tt.layout || p.Preview != tt.wantPreview || p.PreviewWindow != tt.wantWindow || !reflect.DeepEqual(p.Binds, tt.wantBinds) || p.Header != tt.wantHeader {
				t.Errorf("got %+v", p)
			}
			if len(p.Notices) != len(tt.notices) {
				t.Fatalf("notices %q, want %d", p.Notices, len(tt.notices))
			}
			for i, n := range tt.notices {
				if !strings.Contains(p.Notices[i], n) || !strings.HasPrefix(p.Notices[i], "fzf "+tt.v.String()) {
					t.Errorf("notice %d = %q, want %q", i, p.Notices[i], n)
				}
			}
		})
	}
}

func TestPlanUnknownVersionAssumesRecent(t *testing.T) {
	p := PlanFor(Version{}, false, config.Default())
	if p.Layout != "default" || !p.Preview || p.PreviewWindow != config.DefaultPreviewWindow ||
		!reflect.DeepEqual(p.Binds, previewBinds(enterBind, acceptBnd, toggleNew)) || len(p.Notices) != 0 {
		t.Errorf("%+v", p)
	}
}

// Enter opens the preview. Then the movement keys scroll it and Esc closes it.
// With it closed the movement keys move the list and Esc leaves fzf, as always.
func TestEnterOpensTheMovementKeysScrollItEscCloses(t *testing.T) {
	p := PlanFor(Version{0, 74, 4}, true, config.Default())
	if config.DefaultPreviewWindow != "up,50%,wrap,border-bottom,hidden" {
		t.Errorf("default window %q", config.DefaultPreviewWindow)
	}
	byKey := map[string]string{}
	for _, b := range p.Binds {
		k, _, _ := strings.Cut(b, ":")
		byKey[k] = b
	}
	for _, k := range []struct{ key, name string }{
		{"enter", "enter"}, {"esc", "esc"}, {"up", "up"}, {"down", "down"}, {"ctrl-p", "up"}, {"ctrl-n", "down"},
		{"ctrl-k", "up"}, {"ctrl-j", "down"}, {"page-up", "page-up"}, {"page-down", "page-down"}, {"ctrl-/", "toggle"},
	} {
		if byKey[k.key] != hb(k.key, k.name) {
			t.Errorf("%s is bound to %q", k.key, byKey[k.key])
		}
	}
	// What a key does depends on the preview being open, which the helper asks fzf. The
	// binding itself holds no shell code to get wrong.
	for _, b := range p.Binds {
		if strings.Contains(b, "FZF_PREVIEW_LABEL") || strings.Contains(b, "echo") {
			t.Errorf("shell code in a binding: %q", b)
		}
	}
}

func TestAcceptKey(t *testing.T) {
	v := Version{0, 74, 4}
	cfg := config.Default()
	cfg.Keys.Accept = "alt-p"
	if p := PlanFor(v, true, cfg); !reflect.DeepEqual(p.Binds, previewBinds(enterBind, "alt-p:accept", toggleNew)) || p.AcceptKey != "alt-p" {
		t.Errorf("custom accept key: %+v", p)
	}
	// "enter" gives Enter back its usual meaning: no rebinding, no header.
	cfg.Keys.Accept = "enter"
	if p := PlanFor(v, true, cfg); !reflect.DeepEqual(p.Binds, previewBinds(toggleNew)) || p.Header != "" {
		t.Errorf("accept = enter: %+v", p)
	}
	// An empty accept key leaves nothing to print with, but the preview still works.
	cfg.Keys.Accept = ""
	if p := PlanFor(v, true, cfg); !reflect.DeepEqual(p.Binds, previewBinds(enterBind, toggleNew)) || p.AcceptKey != "" {
		t.Errorf("no accept key: %+v", p)
	}
	// Without a hidden preview Enter and the movement keys are not touched.
	cfg = config.Default()
	cfg.PreviewWindow, cfg.PreviewWindowSet = "right,40%", true
	if p := PlanFor(v, true, cfg); !reflect.DeepEqual(p.Binds, []string{toggleOld}) || p.Header != "" {
		t.Errorf("always-visible preview: %+v", p)
	}
}

func TestPlanRespectsUserChoices(t *testing.T) {
	old := Version{0, 20, 0}
	cfg := config.Default()
	cfg.PreviewWindow, cfg.PreviewWindowSet = "right,40%,border-left", true
	cfg.Keys.PreviewToggle = "alt-p"
	p := PlanFor(old, true, cfg)
	if p.PreviewWindow != "right,40%,border-left" || !reflect.DeepEqual(p.Binds, []string{"alt-p:toggle-preview"}) || len(p.Notices) != 0 {
		t.Errorf("a user's own preview_window and key must not be rewritten, and without 'hidden' Enter is left alone: %+v", p)
	}
	// A window of the user's own that does have "hidden" gets Enter/Esc on a new fzf...
	cfg.PreviewWindow = "right,40%,hidden"
	if p := PlanFor(Version{0, 74, 4}, true, cfg); p.PreviewWindow != "right,40%,hidden" || p.Binds[0] != escBind {
		t.Errorf("%+v", p)
	}
	// ...and is left alone (no notice, no rewrite) on one that cannot do it.
	if p := PlanFor(old, true, cfg); p.PreviewWindow != "right,40%,hidden" || len(p.Binds) != 1 || len(p.Notices) != 0 {
		t.Errorf("%+v", p)
	}
	// Below the env var version there is no preview at all, whatever the user set.
	p = PlanFor(Version{0, 17, 4}, true, cfg)
	if p.Preview || p.PreviewWindow != "" || len(p.Binds) != 0 || len(p.Notices) != 1 {
		t.Errorf("%+v", p)
	}
	cfg = config.Default()
	cfg.Preview = false
	if p := PlanFor(Version{0, 74, 4}, true, cfg); p.Preview || len(p.Binds) != 0 || len(p.Notices) != 0 || p.Header != "" || p.Layout != "default" {
		t.Errorf("preview off still puts the prompt at the bottom, and leaves Enter alone: %+v", p)
	}
	cfg.Preview, cfg.Keys.PreviewToggle = true, ""
	if p := PlanFor(Version{0, 74, 4}, true, cfg); !p.Preview || !reflect.DeepEqual(p.Binds, previewBinds(enterBind, acceptBnd)) {
		t.Errorf("no toggle key: %+v", p)
	}
}

func TestWindowHelpers(t *testing.T) {
	if !windowHas("up,50%,hidden", "hidden") || windowHas("up,50%,hiddenish", "hidden") || windowHas("", "hidden") {
		t.Error("windowHas")
	}
	if got := windowWithout("up,50%,wrap,hidden", "hidden"); got != "up,50%,wrap" {
		t.Error(got)
	}
	if got := windowWithout("hidden", "hidden"); got != "" {
		t.Error(got)
	}
}

func TestDescribe(t *testing.T) {
	cfg := config.Default()
	got := strings.Join(Describe(Version{0, 20, 0}, true, cfg), "\n")
	for _, want := range []string{"long lines wrapped in the list (>= 0.54.0): no", "fallback: long lines are cut at the edge of the screen instead of wrapped", "preview pane (>= 0.18.0): yes", "preview border (>= 0.27.0): no", "preview on Enter, closed with Esc (>= 0.49.0): no", "memory kept in step with the buffer, --tail (>= 0.53.0): no", "toggle key ctrl-/ (>= 0.19.0): yes", "fallback: fzf 0.20.0 is too old for the preview border"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if got := strings.Join(Describe(Version{0, 74, 4}, true, cfg), "\n"); !strings.Contains(got, "none needed, full preview layout") {
		t.Error(got)
	}
	if got := strings.Join(Describe(Version{}, false, cfg), "\n"); !strings.Contains(got, "assuming a recent fzf") {
		t.Error(got)
	}
}

// Lines of the list are shown whole, wrapped over several rows, never cut with "..".
func TestLongLinesAreWrappedNotCut(t *testing.T) {
	tests := []struct {
		v     Version
		known bool
		want  []string
	}{
		{Version{0, 74, 4}, true, []string{"--wrap"}},
		{Version{0, 54, 0}, true, []string{"--wrap"}},
		{Version{0, 53, 9}, true, []string{"--no-hscroll"}}, // the best an older fzf can do
		{Version{0, 20, 0}, true, []string{"--no-hscroll"}},
		{Version{}, false, []string{"--wrap"}},
	}
	for _, tt := range tests {
		for _, previewOn := range []bool{true, false} {
			cfg := config.Default()
			cfg.Preview = previewOn
			if p := PlanFor(tt.v, tt.known, cfg); !reflect.DeepEqual(p.ListFlags, tt.want) {
				t.Errorf("fzf %v, preview %v: %v, want %v", tt.v, previewOn, p.ListFlags, tt.want)
			}
		}
	}
	// The user can still turn it off: their options come after ours.
	cfg := config.Default()
	cfg.FzfOpts = "--no-wrap"
	args, _ := BuildArgs(PlanFor(Version{0, 74, 4}, true, cfg), "", cfg, "", NewestLast)
	wrap, noWrap := -1, -1
	for i, a := range args {
		switch a {
		case "--wrap":
			wrap = i
		case "--no-wrap":
			noWrap = i
		}
	}
	if wrap < 0 || noWrap < wrap {
		t.Errorf("--no-wrap must come after --wrap: %q", args)
	}
}
