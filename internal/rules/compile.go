package rules

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Rule is a compiled rule.
type Rule struct {
	Prefix   []byte         // required line prefix, may be empty
	Re       *regexp.Regexp // nil for prefix-only rules, which color the whole line
	Scope    int            // scope id for the whole match, -1 when Captures is used
	Captures []int          // scope id per capture group, -1 to skip
	Stop     bool
}

// Enter is a compiled enter transition.
type Enter struct {
	Re *regexp.Regexp
	To int
}

// Exit is a compiled exit transition.
type Exit struct {
	Re     *regexp.Regexp
	Unless bool
	To     int
}

// Context is a compiled context.
type Context struct {
	Name   string
	Rules  []Rule
	Enters []Enter
	Exits  []Exit
}

// Compiled is a fully compiled rule file. Contexts[0] is "default". Scopes
// are interned: rules refer to them by index.
type Compiled struct {
	Name     string
	Scopes   []string
	Contexts []*Context
}

// Compile validates the file and compiles every regex.
func (f *File) Compile() (*Compiled, error) {
	c, errs := f.build()
	if len(errs) > 0 {
		return nil, errs[0]
	}
	return c, nil
}

// Validate reports every problem in the file: syntax of regexes, scopes,
// captures, unknown contexts, cycles, and sample lines that no rule matches.
func (f *File) Validate() []*Error {
	c, errs := f.build()
	if c != nil && len(errs) == 0 {
		errs = append(errs, f.checkSamples(c)...)
	}
	return errs
}

var scopeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9][a-z0-9_-]*)*$`)
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type builder struct {
	f      *File
	errs   []*Error
	scopes []string
	ids    map[string]int
}

func (b *builder) add(line int, format string, args ...any) {
	b.errs = append(b.errs, b.f.errf(line, format, args...))
}

func (b *builder) scope(line int, s string) int {
	if !scopeRe.MatchString(s) {
		b.add(line, "invalid scope %q (use lowercase dotted names such as level.error)", s)
		return -1
	}
	if id, ok := b.ids[s]; ok {
		return id
	}
	b.ids[s] = len(b.scopes)
	b.scopes = append(b.scopes, s)
	return b.ids[s]
}

func (b *builder) regex(line int, what, src string) *regexp.Regexp {
	re, err := regexp.Compile(src)
	if err != nil {
		msg := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
		b.add(line, "invalid %s regex: %s", what, msg)
		return nil
	}
	return re
}

func (f *File) build() (*Compiled, []*Error) {
	b := &builder{f: f, ids: map[string]int{}}
	if !nameRe.MatchString(f.Name) {
		b.add(f.loc.topKey("name", ""), "invalid name %q", f.Name)
	}
	b.checkDetect()
	if _, ok := f.Context["default"]; !ok {
		b.add(0, `missing "default" context (add [[context.default.rule]] tables)`)
		return nil, b.errs
	}

	names := f.contextNames()
	index := make(map[string]int, len(names))
	for i, n := range names {
		index[n] = i
	}
	out := &Compiled{Name: f.Name}
	for _, name := range names {
		out.Contexts = append(out.Contexts, b.context(name, f.Context[name], index))
	}
	b.checkCycles(names, index)
	out.Scopes = b.scopes
	if len(b.errs) > 0 {
		return nil, b.errs
	}
	return out, nil
}

func (b *builder) checkDetect() {
	f := b.f
	for _, d := range f.Detect {
		b.regex(f.loc.topKey("detect", d), "detect", d)
	}
	for _, g := range f.Filenames {
		if _, err := path.Match(g, ""); err != nil {
			b.add(f.loc.topKey("filenames", g), "invalid filenames glob %q: %v", g, err)
		}
	}
}

func (b *builder) context(name string, def *ContextDef, index map[string]int) *Context {
	ctx := &Context{Name: name}
	for i, r := range def.Rule {
		hdr := fmt.Sprintf("[[context.%s.rule]]", name)
		line := func(key string) int { return b.f.loc.field(hdr, i, key) }
		rule := Rule{Scope: -1, Prefix: []byte(r.Prefix), Stop: r.Stop}
		if r.Regex == "" && r.Prefix == "" {
			b.add(line("regex"), `rule needs "regex" or "prefix"`)
			continue
		}
		if r.Regex != "" {
			rule.Re = b.regex(line("regex"), "rule", r.Regex)
		}
		switch {
		case len(r.Captures) > 0 && r.Scope != "":
			b.add(line("scope"), `use either "scope" or "captures", not both`)
		case len(r.Captures) > 0:
			if rule.Re == nil {
				if r.Regex == "" {
					b.add(line("captures"), `"captures" needs a "regex"`)
				}
				break
			}
			if n := rule.Re.NumSubexp(); n != len(r.Captures) {
				b.add(line("captures"), "regex has %d capture groups but captures lists %d scopes", n, len(r.Captures))
				break
			}
			for _, sc := range r.Captures {
				if sc == "" {
					rule.Captures = append(rule.Captures, -1)
				} else {
					rule.Captures = append(rule.Captures, b.scope(line("captures"), sc))
				}
			}
		case r.Scope == "":
			b.add(line("regex"), `rule needs "scope" or "captures"`)
		default:
			rule.Scope = b.scope(line("scope"), r.Scope)
		}
		ctx.Rules = append(ctx.Rules, rule)
	}
	for i, e := range def.Enter {
		hdr := fmt.Sprintf("[[context.%s.enter]]", name)
		line := func(key string) int { return b.f.loc.field(hdr, i, key) }
		to, ok := index[e.To]
		if !ok {
			b.add(line("to"), "enter in context %q goes to unknown context %q", name, e.To)
			continue
		}
		if e.Regex == "" {
			b.add(line("regex"), `enter needs a "regex"`)
			continue
		}
		if re := b.regex(line("regex"), "enter", e.Regex); re != nil {
			ctx.Enters = append(ctx.Enters, Enter{Re: re, To: to})
		}
	}
	for i, x := range def.Exit {
		hdr := fmt.Sprintf("[[context.%s.exit]]", name)
		line := func(key string) int { return b.f.loc.field(hdr, i, key) }
		dest := x.To
		if dest == "" {
			dest = "default"
		}
		to, ok := index[dest]
		if !ok {
			b.add(line("to"), "exit in context %q goes to unknown context %q", name, dest)
			continue
		}
		src, unless := x.Regex, false
		switch {
		case x.Regex != "" && x.Unless != "":
			b.add(line("regex"), `use either "regex" or "unless" in an exit, not both`)
			continue
		case x.Unless != "":
			src, unless = x.Unless, true
		case x.Regex == "":
			b.add(line("regex"), `exit needs "regex" or "unless"`)
			continue
		}
		key := "regex"
		if unless {
			key = "unless"
		}
		if re := b.regex(line(key), "exit", src); re != nil {
			ctx.Exits = append(ctx.Exits, Exit{Re: re, Unless: unless, To: to})
		}
	}
	return ctx
}

// checkCycles rejects cycles in the enter transitions (a context that enters
// itself, or contexts that enter each other). Exit transitions lead back to a
// context that does not depend on the one being left, so they are not edges.
func (b *builder) checkCycles(names []string, index map[string]int) {
	edges := make([][]int, len(names))
	for i, n := range names {
		for _, e := range b.f.Context[n].Enter {
			if to, ok := index[e.To]; ok {
				edges[i] = append(edges[i], to)
			}
		}
	}
	state := make([]int, len(names)) // 0 new, 1 on stack, 2 done
	var stack []int
	reported := false
	var visit func(int)
	visit = func(i int) {
		state[i] = 1
		stack = append(stack, i)
		for _, to := range edges[i] {
			if reported {
				break
			}
			switch state[to] {
			case 0:
				visit(to)
			case 1:
				cyc := []string{}
				start := 0
				for k, s := range stack {
					if s == to {
						start = k
					}
				}
				for _, s := range stack[start:] {
					cyc = append(cyc, names[s])
				}
				cyc = append(cyc, names[to])
				b.add(b.f.loc.contextHeader(names[to]), "context cycle: %s", strings.Join(cyc, " -> "))
				reported = true
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = 2
	}
	for i := range names {
		if state[i] == 0 && !reported {
			visit(i)
		}
	}
}

// checkSamples verifies that every sample line is matched by at least one rule.
func (f *File) checkSamples(c *Compiled) []*Error {
	var errs []*Error
	for _, s := range f.Sample {
		line := []byte(s)
		if !c.MatchesAny(line) {
			errs = append(errs, f.errf(f.loc.topKey("sample", s), "sample line is not matched by any rule: %q", s))
		}
	}
	return errs
}

// MatchesAny reports whether any rule in any context matches line.
func (c *Compiled) MatchesAny(line []byte) bool {
	for _, ctx := range c.Contexts {
		for i := range ctx.Rules {
			if ctx.Rules[i].Matches(line) {
				return true
			}
		}
	}
	return false
}

// Matches reports whether the rule applies to line.
func (r *Rule) Matches(line []byte) bool {
	if len(r.Prefix) > 0 && !hasPrefix(line, r.Prefix) {
		return false
	}
	return r.Re == nil || r.Re.Match(line)
}

func hasPrefix(b, p []byte) bool {
	return len(b) >= len(p) && string(b[:len(p)]) == string(p)
}
