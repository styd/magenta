package rules

import (
	"path"
	"path/filepath"
	"regexp"

	"github.com/styd/magenta/internal/config"
)

// Pattern is a compiled detect regex.
type Pattern struct {
	Src string
	Re  *regexp.Regexp
}

// Detector is what auto-detection needs from a rule file. Building one
// compiles only the detect regexes, not the whole rule file.
type Detector struct {
	Name      string
	Path      string
	Builtin   bool
	Patterns  []Pattern
	Filenames []string
}

// MatchFilename returns the first glob that matches the base name of file.
func (d *Detector) MatchFilename(file string) (string, bool) {
	base := filepath.Base(file) // also splits at a backslash on Windows
	for _, g := range d.Filenames {
		if ok, _ := path.Match(g, base); ok {
			return g, true
		}
	}
	return "", false
}

// Loader supplies rule files. It is an interface so that a compiled cache can
// be added later without touching its users. None exists today.
type Loader interface {
	// Detectors returns a detector for every rule file that parses. Files
	// with problems are skipped and returned in the second value.
	Detectors() ([]*Detector, []error)
	// Load fully compiles the named rule file.
	Load(name string) (*Compiled, error)
	// File returns the parsed (not compiled) rule file.
	File(name string) (*File, error)
}

// ConfigLoader reads built-in rules and the user's overrides.
type ConfigLoader struct{}

// Detectors implements Loader.
func (ConfigLoader) Detectors() ([]*Detector, []error) {
	files, err := config.List(config.Rules)
	if err != nil {
		return nil, []error{err}
	}
	var out []*Detector
	var errs []error
	for _, cf := range files {
		f, err := Parse(cf.Path, cf.Data)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		d, err := f.Detector()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		d.Builtin = cf.Builtin
		out = append(out, d)
	}
	return out, errs
}

// File implements Loader.
func (ConfigLoader) File(name string) (*File, error) {
	cf, err := config.Find(config.Rules, name)
	if err != nil {
		return nil, err
	}
	return Parse(cf.Path, cf.Data)
}

// Load implements Loader.
func (l ConfigLoader) Load(name string) (*Compiled, error) {
	f, err := l.File(name)
	if err != nil {
		return nil, err
	}
	return f.Compile()
}

// Detector compiles the detect regexes and filename globs.
func (f *File) Detector() (*Detector, error) {
	d := &Detector{Name: f.Name, Path: f.Path, Filenames: f.Filenames}
	for _, src := range f.Detect {
		re, err := regexp.Compile(src)
		if err != nil {
			return nil, f.errf(f.loc.topKey("detect", src), "invalid detect regex: %v", err)
		}
		d.Patterns = append(d.Patterns, Pattern{src, re})
	}
	for _, g := range f.Filenames {
		if _, err := path.Match(g, ""); err != nil {
			return nil, f.errf(f.loc.topKey("filenames", g), "invalid filenames glob %q: %v", g, err)
		}
	}
	return d, nil
}
