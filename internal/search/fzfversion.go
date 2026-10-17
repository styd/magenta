// Package search runs fzf over the lines mgnt has seen.
package search

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/styd/magenta/internal/config"
)

// Version is an fzf release number.
type Version struct{ Major, Minor, Patch int }

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Less reports whether v is older than o.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// The fzf releases that introduced what mgnt uses. All of these were checked
// against the history of github.com/junegunn/fzf (the first release tag that
// contains the commit adding each feature). Several differ from the numbers
// quoted from an unofficial changelog mirror, which is why they are not taken
// on trust.
var (
	// VerPreview: --preview, --preview-window, and the toggle-preview action
	// (commit 2bbc1206, "Add --preview and --preview-window").
	VerPreview = Version{0, 13, 0}
	// VerPreviewWrap: the wrap flag of --preview-window (a442fe0f). The mirror
	// said 0.15.2. It is older than VerPreviewEnv, so it never limits us.
	VerPreviewWrap = Version{0, 16, 0}
	// VerPreviewEnv: $FZF_PREVIEW_LINES and $FZF_PREVIEW_COLUMNS (8dc1377e).
	// Without them the preview helper cannot learn the pane size.
	VerPreviewEnv = Version{0, 18, 0}
	// VerKeyChords: "ctrl-/" and similar key names in --bind (168453da). fzf
	// refuses to start when --bind names a key it does not know.
	VerKeyChords = Version{0, 19, 0}
	// VerBorderTop: border-top, border-bottom, and the other border-* styles of
	// --preview-window (15f4cfb6).
	VerBorderTop = Version{0, 27, 0}
	// VerPreviewState: what the Enter/Esc preview needs. The show-preview and
	// hide-preview actions (0.38.0, aa2b9ec4), change-preview-label (0.37.0)
	// and the transform action (0.45.0) are older. The limiting piece is the
	// $FZF_PREVIEW_LABEL variable (f625c5aa), which lets a binding tell whether
	// the preview is open. The unofficial mirror gave 0.38.0 for toggle-preview,
	// which is wrong: that number belongs to show-preview.
	VerPreviewState = Version{0, 49, 0}
	// VerListWrap: --wrap, which shows long lines of the list in full over
	// several rows (70bf8bc3). Before it, fzf cuts a long line at the edge of the
	// screen and marks the cut with "..", and scrolls it sideways to the match.
	// --no-hscroll (0.9.10) at least stops the scrolling.
	VerListWrap = Version{0, 54, 0}
	// VerTail: --tail=NUM, which makes fzf keep only the newest NUM items in
	// memory (commit 93bbb30, "Add --tail=NUM to limit the number of items to
	// keep in memory", first in the 0.53.0 tag). The commit drops the oldest
	// chunks from fzf's own list, in arrival order.
	VerTail = Version{0, 53, 0}
	// VerInputColors: the input-bg and input-fg color names, which the "/" search
	// bar of the colorscheme needs. "input-bg" is not in src/options.go at the
	// v0.57.0 tag and is there at v0.58.0.
	VerInputColors = Version{0, 58, 0}
	// VerSeparator and VerScrollbar: the separator and scrollbar color names.
	// "separator" is not in src/options.go at the 0.34.0 tag and is there at
	// 0.35.0. "scrollbar" is not there at 0.35.0 and is there at 0.36.0. The other
	// names the colorscheme sets (fg+, bg+, pointer, marker, header, info, border,
	// gutter) are in the source at 0.20.0 already.
	VerSeparator = Version{0, 35, 0}
	VerScrollbar = Version{0, 36, 0}
	// VerLayout: the --layout option (daa1958f).
	VerLayout = Version{0, 17, 4}
	// The "change" event, used to hide the preview while typing, is older
	// (0.16.8) than anything else we need, so it has no constant.
)

// DefaultToggleKey is the preview toggle key unless configured otherwise. It
// is unbound in fzf itself.
const DefaultToggleKey = "ctrl-/"

// previewLabel is shown on the border of an open preview. It tells the user
// how to use it, and it is also how the keys know the preview is open: fzf
// exports it as $FZF_PREVIEW_LABEL.
const previewLabel = " Up/Down: scroll   Esc: back   (list paused) "

const (
	hideAction = "hide-preview+change-preview-label()"
	showAction = "show-preview+change-preview-label(" + previewLabel + ")"
)

// helperToken stands for the command that reaches the preview server in the
// bindings of a Plan. It is only known once the server is running (it holds
// the socket path), and BuildArgs puts it in.
const helperToken = "{HELPER}"

// previewKeys are the keys that run the helper while the preview exists, with
// the name the helper knows each by. Closed preview: they do what they always
// do. Open preview: the movement keys scroll it.
var previewKeys = []struct{ key, name string }{
	{"esc", "esc"},
	{"up", "up"}, {"down", "down"},
	{"ctrl-p", "up"}, {"ctrl-n", "down"},
	{"ctrl-k", "up"}, {"ctrl-j", "down"},
	{"page-up", "page-up"}, {"page-down", "page-down"},
	// The mouse wheel over the preview (fzf 0.43 and later).
	{"preview-scroll-up", "wheel-up"}, {"preview-scroll-down", "wheel-down"},
}

func helperBind(key, name string) string {
	return key + ":transform:" + helperToken + " --key " + name
}

// ParseVersion reads the leading MAJOR.MINOR.PATCH of `fzf --version` output
// such as "0.54.3 (brew)", "0.54.3 (devel)" or "0.9.4". A missing patch
// number counts as 0.
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimSpace(s)
	s, _, _ = strings.Cut(s, " ")
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Version{}, false
		}
		n[i] = v
	}
	return Version{n[0], n[1], n[2]}, true
}

// Plan says which layout and preview flags to give fzf for a version.
type Plan struct {
	Tail          int      // value for --tail, 0 for none. Set by the Searcher, which knows the ring
	Layout        string   // value for --layout, "" for none
	ListFlags     []string // how long lines are shown in the list
	Preview       bool     // pass --preview
	PreviewWindow string   // value for --preview-window, "" for none
	Binds         []string // values for --bind
	Colors        string   // value for --color, "" for none
	Header        string   // value for --header, "" for none. See AcceptKey
	AcceptKey     string   // the key that accepts, which Header describes once the mode is known
	Notices       []string
}

// Color is one color of fzf's --color, such as {"input-bg", "#2d2547"}.
type Color struct{ Name, Value string }

// colorSince is the first fzf version that knows a color name. A name that is
// not here is older than any fzf that mgnt works with.
var colorSince = map[string]Version{
	"input-bg":  VerInputColors,
	"input-fg":  VerInputColors,
	"separator": VerSeparator,
	"scrollbar": VerScrollbar,
}

// colorArg makes the value of --color from a base scheme ("dark", "light", or
// "") and colors, leaving out those that fzf v does not know. known false means
// the version could not be read, which is taken as recent.
func colorArg(base string, colors []Color, v Version, known bool) string {
	var parts []string
	if base != "" && len(colors) > 0 {
		parts = append(parts, base)
	}
	for _, c := range colors {
		if since, ok := colorSince[c.Name]; ok && known && v.Less(since) {
			continue
		}
		parts = append(parts, c.Name+":"+c.Value)
	}
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == base) {
		return ""
	}
	return strings.Join(parts, ",")
}

// windowHas reports whether a --preview-window value contains the flag.
func windowHas(window, flag string) bool {
	return slices.Contains(strings.Split(window, ","), flag)
}

// windowWithout removes a flag from a --preview-window value.
func windowWithout(window, flag string) string {
	var keep []string
	for _, f := range strings.Split(window, ",") {
		if f != flag {
			keep = append(keep, f)
		}
	}
	return strings.Join(keep, ",")
}

// SupportsTail says whether this fzf has --tail. An unreadable version is
// assumed to be recent, like everywhere else.
func SupportsTail(v Version, known bool) bool { return !known || !v.Less(VerTail) }

// PlanFor decides the layout and preview flags for an fzf version. known is
// false when the version could not be read, in which case a recent fzf is
// assumed. A user who set preview_window in config.toml gets exactly that
// value: they take responsibility for it. Search itself is never refused.
//
// The prompt is put at the bottom (--layout default). When the preview window
// has the "hidden" flag, as the default does, Enter on a line opens the preview
// and Esc closes it. With the preview closed, Esc leaves fzf as usual. Enter
// then no longer accepts, so the accept key prints the selected lines.
func PlanFor(v Version, known bool, cfg config.Config) Plan {
	var p Plan
	if !known || !v.Less(VerLayout) {
		p.Layout = "default"
	}
	// Lines are shown whole, wrapped over as many rows as they need. An older
	// fzf can only cut them at the edge of the screen.
	if !known || !v.Less(VerListWrap) {
		p.ListFlags = []string{"--wrap"}
	} else {
		p.ListFlags = []string{"--no-hscroll"}
	}
	if !cfg.Preview {
		return p
	}
	if known && v.Less(VerPreviewEnv) {
		p.Notices = append(p.Notices, fmt.Sprintf("fzf %s is too old for the preview pane (needs >= %s), searching without it", v, VerPreviewEnv))
		return p
	}
	p.Preview = true
	win := cfg.PreviewWindow
	if known && !cfg.PreviewWindowSet {
		if v.Less(VerBorderTop) && windowHas(win, "border-bottom") {
			win = windowWithout(win, "border-bottom")
			p.Notices = append(p.Notices, fmt.Sprintf("fzf %s is too old for the preview border (needs >= %s), showing the preview pane without it", v, VerBorderTop))
		}
		if v.Less(VerPreviewState) && windowHas(win, "hidden") {
			win = windowWithout(win, "hidden")
			p.Notices = append(p.Notices, fmt.Sprintf("fzf %s cannot open the preview with Enter and close it with Esc (needs >= %s), so the preview is always shown", v, VerPreviewState))
		}
	}
	p.PreviewWindow = win

	stateful := windowHas(win, "hidden") && (!known || !v.Less(VerPreviewState))
	accept := cfg.Keys.Accept
	if stateful {
		for _, k := range previewKeys {
			p.Binds = append(p.Binds, helperBind(k.key, k.name))
		}
		if accept != "enter" {
			p.Binds = append(p.Binds, helperBind("enter", "enter"))
			p.Header = "enter: context   esc: back   tab: mark"
			if accept != "" {
				p.Binds = append(p.Binds, accept+":accept")
				p.AcceptKey = accept
			}
		}
	}

	key := cfg.Keys.PreviewToggle
	switch {
	case key == "":
	case known && key == DefaultToggleKey && v.Less(VerKeyChords):
		p.Notices = append(p.Notices, fmt.Sprintf("fzf %s cannot bind %s (needs >= %s), so the preview has no toggle key", v, key, VerKeyChords))
	case stateful:
		p.Binds = append(p.Binds, helperBind(key, "toggle"))
	default:
		p.Binds = append(p.Binds, key+":toggle-preview")
	}
	return p
}

// Describe lists, for `magenta doctor`, what an fzf version supports and
// which fallback applies.
func Describe(v Version, known bool, cfg config.Config) []string {
	if !known {
		return []string{"version not recognized: assuming a recent fzf, full preview"}
	}
	yes := func(ok bool) string {
		if ok {
			return "yes"
		}
		return "no"
	}
	out := []string{
		fmt.Sprintf("preview pane (>= %s): %s", VerPreviewEnv, yes(!v.Less(VerPreviewEnv))),
		fmt.Sprintf("preview border (>= %s): %s", VerBorderTop, yes(!v.Less(VerBorderTop))),
		fmt.Sprintf("preview on Enter, closed with Esc (>= %s): %s", VerPreviewState, yes(!v.Less(VerPreviewState))),
		fmt.Sprintf("toggle key %s (>= %s): %s", DefaultToggleKey, VerKeyChords, yes(!v.Less(VerKeyChords))),
		fmt.Sprintf("long lines wrapped in the list (>= %s): %s", VerListWrap, yes(!v.Less(VerListWrap))),
		fmt.Sprintf("memory kept in step with the buffer, --tail (>= %s): %s", VerTail, yes(!v.Less(VerTail))),
	}
	if v.Less(VerListWrap) {
		out = append(out, "fallback: long lines are cut at the edge of the screen instead of wrapped")
	}
	plan := PlanFor(v, true, cfg)
	switch {
	case !cfg.Preview:
		out = append(out, "fallback: none needed, the preview is turned off in config.toml")
	case len(plan.Notices) == 0:
		out = append(out, "fallback: none needed, full preview layout")
	default:
		for _, n := range plan.Notices {
			out = append(out, "fallback: "+n)
		}
	}
	return out
}
