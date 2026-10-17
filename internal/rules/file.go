// Package rules parses, validates, and compiles rule files.
package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/styd/magenta/internal/config"
)

// RuleDef is one [[context.<name>.rule]] table.
type RuleDef struct {
	Regex    string   `toml:"regex"`
	Prefix   string   `toml:"prefix"` // line must start with this. With no regex the whole line gets Scope
	Scope    string   `toml:"scope"`
	Captures []string `toml:"captures"` // one scope per capture group, "" to skip a group
	Stop     bool     `toml:"stop"`     // when this rule matches, no later rule runs on the line
}

// EnterDef is one [[context.<name>.enter]] table: when the line matches, the
// following lines are processed in context To.
type EnterDef struct {
	Regex string `toml:"regex"`
	To    string `toml:"to"`
}

// ExitDef is one [[context.<name>.exit]] table. The context is left when the
// line matches Regex, or, with Unless, when it does not match. The line is
// then processed again in context To (default "default").
type ExitDef struct {
	Regex  string `toml:"regex"`
	Unless string `toml:"unless"`
	To     string `toml:"to"`
}

// ContextDef is a named set of rules and transitions.
type ContextDef struct {
	Rule  []RuleDef  `toml:"rule"`
	Enter []EnterDef `toml:"enter"`
	Exit  []ExitDef  `toml:"exit"`
}

// File is a parsed (but not yet compiled) rule file.
type File struct {
	Name      string                 `toml:"name"`
	Detect    []string               `toml:"detect"`
	Filenames []string               `toml:"filenames"`
	Sample    []string               `toml:"sample"`
	Context   map[string]*ContextDef `toml:"context"`

	Path string `toml:"-"`
	loc  *locator
}

// Error is a problem in a rule file, reported as path:line: message.
type Error struct {
	Path string
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Msg)
}

// Parse reads the TOML of a rule file. path is only used in error messages.
func Parse(path string, data []byte) (*File, error) {
	f := &File{Path: path, loc: newLocator(data)}
	md, err := toml.Decode(string(data), f)
	if err != nil {
		return nil, &Error{path, config.ParseErrorLine(err), err.Error()}
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, &Error{path, f.loc.anyKey(un[0]), "unknown key " + un[0].String()}
	}
	if f.Name == "" {
		return nil, &Error{path, 0, `missing "name"`}
	}
	return f, nil
}

func (f *File) errf(line int, format string, args ...any) *Error {
	return &Error{f.Path, line, fmt.Sprintf(format, args...)}
}

// contextNames lists the contexts with "default" first, the rest sorted.
func (f *File) contextNames() []string {
	names := make([]string, 0, len(f.Context))
	for n := range f.Context {
		if n != "default" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if _, ok := f.Context["default"]; ok {
		names = append([]string{"default"}, names...)
	}
	return names
}

// locator finds line numbers in the TOML source, which the decoder does not
// report for values.
type locator struct {
	lines   []string
	headers map[string][]int // trimmed header line -> 0-based line of each occurrence
}

func newLocator(src []byte) *locator {
	l := &locator{lines: strings.Split(string(src), "\n"), headers: map[string][]int{}}
	for i, ln := range l.lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") {
			l.headers[t] = append(l.headers[t], i)
		}
	}
	return l
}

// field returns the 1-based line of key in the idx-th table with this header,
// or the table's own line when the key is missing.
func (l *locator) field(header string, idx int, key string) int {
	starts := l.headers[header]
	if idx >= len(starts) {
		return 0
	}
	for i := starts[idx] + 1; i < len(l.lines); i++ {
		t := strings.TrimSpace(l.lines[i])
		if strings.HasPrefix(t, "[") {
			break
		}
		if strings.HasPrefix(t, key) && strings.HasPrefix(strings.TrimSpace(t[len(key):]), "=") {
			return i + 1
		}
	}
	return starts[idx] + 1
}

// topKey returns the line of a top-level key such as detect, and, for list
// values, the line holding item (the first line from there that contains it).
func (l *locator) topKey(key, item string) int {
	for i, ln := range l.lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") {
			break
		}
		if strings.HasPrefix(t, key) && strings.HasPrefix(strings.TrimSpace(t[len(key):]), "=") {
			if item != "" {
				for j := i; j < len(l.lines); j++ {
					if strings.Contains(l.lines[j], item) {
						return j + 1
					}
					if strings.Contains(l.lines[j], "]") && j > i {
						break
					}
				}
			}
			return i + 1
		}
	}
	return 0
}

func (l *locator) anyKey(k toml.Key) int {
	last := k[len(k)-1]
	for i, ln := range l.lines {
		if strings.Contains(ln, last) {
			return i + 1
		}
	}
	return 0
}

func (l *locator) contextHeader(name string) int {
	for h, idx := range l.headers {
		if strings.HasPrefix(h, "[[context."+name+".") || h == "[context."+name+"]" {
			return idx[0] + 1
		}
	}
	return 0
}
