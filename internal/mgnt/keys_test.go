package mgnt

import (
	"reflect"
	"testing"
)

func parse(in string) []key {
	var p keyParser
	var out []key
	for _, b := range []byte(in) {
		if k, ok := p.feed(b); ok {
			out = append(out, k)
		}
	}
	return out
}

func TestKeyParser(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []key
	}{
		{"plain keys", "jk q", []key{'j', 'k', ' ', 'q'}},
		{"control keys", "\x06\x02\r\n", []key{6, 2, '\r', '\n'}},
		{"arrows", "\x1b[A\x1b[B", []key{keyUp, keyDown}},
		{"arrows in application mode", "\x1bOA\x1bOB", []key{keyUp, keyDown}},
		{"arrows with modifiers", "\x1b[1;5A\x1b[1;2B", []key{keyUp, keyDown}},
		{"home and end", "\x1b[H\x1b[F\x1bOH\x1bOF", []key{keyHome, keyEnd, keyHome, keyEnd}},
		{"tilde forms", "\x1b[1~\x1b[4~\x1b[5~\x1b[6~\x1b[7~\x1b[8~", []key{keyHome, keyEnd, keyPgUp, keyPgDn, keyHome, keyEnd}},
		{"left and right", "\x1b[C\x1b[D\x1bOC\x1bOD", []key{keyRight, keyLeft, keyRight, keyLeft}},
		{"keys we do not use are dropped whole", "\x1b[15~\x1b[2~\x1b[3~x", []key{'x'}},
		{"a lone ESC is dropped and the next key counts", "\x1bq", []key{'q'}},
		{"ESC ESC then a sequence", "\x1b\x1b[A", []key{keyUp}},
		{"mixed", "j\x1b[Bk\x1b[5~/", []key{'j', keyDown, 'k', keyPgUp, '/'}},
		{"an unfinished sequence waits", "\x1b[", nil},
		{"a reply to a question is not a key", "\x1b]11;rgb:1e1e/1e1e/2e2e\x1b\\q", []key{'q'}},
		{"a reply ended by BEL", "\x1b]11;rgb:ffff/ffff/ffff\x07/", []key{'/'}},
		{"keys around a reply", "j\x1b]11;rgb:0000/0000/0000\x1b\\k", []key{'j', 'k'}},
	}
	for _, tt := range tests {
		if got := parse(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
	// A sequence split over reads is the same as one read.
	var p keyParser
	p.feed(0x1b)
	p.feed('[')
	if k, ok := p.feed('A'); !ok || k != keyUp {
		t.Errorf("split sequence: %v %v", k, ok)
	}
}
