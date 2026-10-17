// Package background finds out whether the terminal has a dark or a light
// background, so that the right theme (dark or light) can be chosen.
package background

import (
	"strconv"
	"strings"
	"time"
)

// The themes. "auto" is the setting that asks the terminal to choose between
// the other two.
const (
	Auto  = "auto"
	Dark  = "dark"
	Light = "light"
)

// Timeout is how long to wait for a terminal to answer. A terminal that
// supports the question answers in a few milliseconds, so this is only ever
// paid by one that ignores it.
const Timeout = 150 * time.Millisecond

// Where an answer came from.
const (
	SourceOverride  = "MGNT_BACKGROUND"
	SourceTerminal  = "the terminal's answer"
	SourceColorFgBg = "COLORFGBG"
	SourceAssumed   = "assumed"
)

// Result is what was found out.
type Result struct {
	Dark   bool
	Source string
}

// Theme is the theme that suits this background: "dark" or "light".
func (r Result) Theme() string {
	if r.Dark {
		return Dark
	}
	return Light
}

func (r Result) String() string {
	kind := "light"
	if r.Dark {
		kind = "dark"
	}
	if r.Source == SourceAssumed {
		return kind + " (assumed, nothing told the background)"
	}
	return kind + " (from " + r.Source + ")"
}

// Asker can send a request to the terminal and read its reply. A terminal
// that does not answer within the timeout gives ok false.
type Asker interface {
	Query(request string, timeout time.Duration) (reply string, ok bool)
}

// queryBackground asks for the background color: OSC 11 with a "?" as its value.
const queryBackground = "\x1b]11;?\x1b\\"

// Detect decides dark or light. In order: the MGNT_BACKGROUND variable
// (dark or light), the terminal's own answer (when asker is not nil), the
// COLORFGBG variable, and otherwise dark, which is what most developers use.
func Detect(asker Asker, getenv func(string) string) Result {
	switch strings.ToLower(strings.TrimSpace(getenv("MGNT_BACKGROUND"))) {
	case "dark":
		return Result{true, SourceOverride}
	case "light":
		return Result{false, SourceOverride}
	}
	if asker != nil {
		if reply, ok := asker.Query(queryBackground, Timeout); ok {
			if r, g, b, ok := ParseReply(reply); ok {
				return Result{IsDark(r, g, b), SourceTerminal}
			}
		}
	}
	if dark, ok := FromColorFgBg(getenv("COLORFGBG")); ok {
		return Result{dark, SourceColorFgBg}
	}
	return Result{true, SourceAssumed}
}

// ParseReply reads the terminal's answer to the OSC 11 question:
//
//	ESC ] 11 ; rgb:RRRR/GGGG/BBBB ESC \     (or ended by BEL)
//
// Each part has one to four hex digits. The result is each color from 0 to 1.
func ParseReply(reply string) (r, g, b float64, ok bool) {
	i := strings.Index(reply, "rgb:")
	if i < 0 {
		return 0, 0, 0, false
	}
	rest := reply[i+len("rgb:"):]
	// The color ends at the terminator of the sequence.
	if j := strings.IndexAny(rest, "\x07\x1b"); j >= 0 {
		rest = rest[:j]
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var c [3]float64
	for k, p := range parts {
		if len(p) < 1 || len(p) > 4 {
			return 0, 0, 0, false
		}
		v, err := strconv.ParseUint(p, 16, 16)
		if err != nil {
			return 0, 0, 0, false
		}
		max := float64(uint64(1)<<(4*uint(len(p))) - 1)
		c[k] = float64(v) / max
	}
	return c[0], c[1], c[2], true
}

// IsDark says whether a color (each part from 0 to 1) is dark, by its
// perceived brightness.
func IsDark(r, g, b float64) bool { return 0.299*r+0.587*g+0.114*b < 0.5 }

// FromColorFgBg reads the COLORFGBG variable, which some terminals set to
// "foreground;background" (or with a middle field) in terms of the 16 ANSI
// colors. Backgrounds 0 to 6 and 8 are dark, 7 and 9 to 15 are light.
func FromColorFgBg(v string) (dark, ok bool) {
	if v == "" {
		return false, false
	}
	fields := strings.Split(v, ";")
	n, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil || n < 0 || n > 15 {
		return false, false
	}
	return n <= 6 || n == 8, true
}
