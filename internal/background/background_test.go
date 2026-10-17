package background

import (
	"testing"
	"time"
)

type fakeAsker struct {
	reply string
	ok    bool
	calls int
}

func (f *fakeAsker) Query(request string, timeout time.Duration) (string, bool) {
	f.calls++
	return f.reply, f.ok
}

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestParseReply(t *testing.T) {
	tests := []struct {
		reply string
		dark  bool
		ok    bool
	}{
		{"\x1b]11;rgb:0000/0000/0000\x1b\\", true, true},
		{"\x1b]11;rgb:ffff/ffff/ffff\x07", false, true},
		{"\x1b]11;rgb:1f1f/1919/3333\x1b\\", true, true},  // the dark background of the magenta colorscheme
		{"\x1b]11;rgb:f7f7/f3f3/ffff\x1b\\", false, true}, // and its light one
		{"\x1b]11;rgb:ff/ff/ff\x07", false, true},         // two digits per part
		{"\x1b]11;rgb:f/f/f\x07", false, true},            // one digit per part
		{"\x1b]11;rgb:8000/8000/8000\x07", false, true},
		{"\x1b]11;rgb:7fff/7fff/7fff\x07", true, true},
		{"\x1b]11;rgb:zz/00/00\x07", false, false},
		{"\x1b]11;rgb:00/00\x07", false, false},
		{"\x1b]11;rgb:00000/00/00\x07", false, false},
		{"hello", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		r, g, b, ok := ParseReply(tt.reply)
		if ok != tt.ok {
			t.Errorf("%q: ok %v, want %v", tt.reply, ok, tt.ok)
			continue
		}
		if ok && IsDark(r, g, b) != tt.dark {
			t.Errorf("%q: dark %v, want %v", tt.reply, !tt.dark, tt.dark)
		}
	}
}

func TestFromColorFgBg(t *testing.T) {
	tests := []struct {
		v    string
		dark bool
		ok   bool
	}{
		{"15;0", true, true},
		{"0;15", false, true},
		{"0;default;15", false, true},
		{"15;8", true, true},
		{"0;7", false, true},
		{"0;9", false, true},
		{"", false, false},
		{"15;default", false, false},
		{"15;99", false, false},
	}
	for _, tt := range tests {
		dark, ok := FromColorFgBg(tt.v)
		if dark != tt.dark || ok != tt.ok {
			t.Errorf("%q: got %v %v, want %v %v", tt.v, dark, ok, tt.dark, tt.ok)
		}
	}
}

func TestDetectOrder(t *testing.T) {
	light := "\x1b]11;rgb:ffff/ffff/ffff\x07"
	dark := "\x1b]11;rgb:0000/0000/0000\x07"
	tests := []struct {
		name   string
		asker  *fakeAsker
		env    func(string) string
		dark   bool
		source string
		asked  bool
	}{
		{"override wins over the terminal", &fakeAsker{dark, true, 0}, env("MGNT_BACKGROUND", "Light"), false, SourceOverride, false},
		{"override dark", &fakeAsker{light, true, 0}, env("MGNT_BACKGROUND", "dark", "COLORFGBG", "0;15"), true, SourceOverride, false},
		{"terminal wins over COLORFGBG", &fakeAsker{light, true, 0}, env("COLORFGBG", "15;0"), false, SourceTerminal, true},
		{"terminal says dark", &fakeAsker{dark, true, 0}, env(), true, SourceTerminal, true},
		{"no answer falls back to COLORFGBG", &fakeAsker{"", false, 0}, env("COLORFGBG", "0;15"), false, SourceColorFgBg, true},
		{"garbage falls back to COLORFGBG", &fakeAsker{"nonsense", true, 0}, env("COLORFGBG", "0;15"), false, SourceColorFgBg, true},
		{"nothing at all assumes dark", &fakeAsker{"", false, 0}, env(), true, SourceAssumed, true},
		{"an unknown override is ignored", &fakeAsker{light, true, 0}, env("MGNT_BACKGROUND", "blue"), false, SourceTerminal, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Detect(tt.asker, tt.env)
			if r.Dark != tt.dark || r.Source != tt.source || (tt.asker.calls > 0) != tt.asked {
				t.Errorf("got %+v after %d questions", r, tt.asker.calls)
			}
		})
	}
}

func TestDetectWithoutAsker(t *testing.T) {
	if r := Detect(nil, env("COLORFGBG", "0;15")); r.Dark || r.Source != SourceColorFgBg {
		t.Errorf("got %+v", r)
	}
	if r := Detect(nil, env()); !r.Dark || r.Source != SourceAssumed {
		t.Errorf("got %+v", r)
	}
}

func TestThemeAndString(t *testing.T) {
	if (Result{true, SourceOverride}).Theme() != Dark || (Result{false, SourceOverride}).Theme() != Light {
		t.Error("wrong theme")
	}
	if got := (Result{false, SourceColorFgBg}).String(); got != "light (from COLORFGBG)" {
		t.Errorf("got %q", got)
	}
	if got := (Result{true, SourceAssumed}).String(); got != "dark (assumed, nothing told the background)" {
		t.Errorf("got %q", got)
	}
}
