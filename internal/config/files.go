package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/styd/magenta"
)

// Kind is a directory of user-editable TOML files.
type Kind string

const (
	Rules        Kind = "rules"
	Colorschemes Kind = "colorschemes"
)

// File is a rule or colorscheme file, built-in or from the user's config dir.
type File struct {
	Name     string
	Path     string // file path, or "builtin/<kind>/<name>.toml"
	Data     []byte
	Builtin  bool // embedded in the binary
	Override bool // a user file that replaces a built-in of the same name
}

// UserDir returns the directory holding the user's files of this kind.
func UserDir(k Kind) string { return filepath.Join(Dir(), string(k)) }

func nameOf(data []byte) string {
	var n struct {
		Name string `toml:"name"`
	}
	// Errors are reported later by the real parser, which knows the file.
	_, _ = toml.Decode(string(data), &n)
	return n.Name
}

// BuiltinNames lists the embedded files of this kind, sorted.
func BuiltinNames(k Kind) []string {
	ents, _ := magenta.FS.ReadDir(string(k))
	var out []string
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".toml"); ok {
			out = append(out, n)
		}
	}
	return out
}

// Builtin returns the embedded file with this name.
func Builtin(k Kind, name string) (File, error) {
	p := path.Join(string(k), name+".toml")
	data, err := magenta.FS.ReadFile(p)
	if err != nil {
		return File{}, fmt.Errorf("no built-in %s %q", strings.TrimSuffix(string(k), "s"), name)
	}
	return File{Name: name, Path: "builtin/" + p, Data: data, Builtin: true}, nil
}

func userFiles(k Kind) []File {
	dir := UserDir(k)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []File
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		name := nameOf(data)
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".toml")
		}
		out = append(out, File{Name: name, Path: p, Data: data})
	}
	return out
}

// Find returns the file called name. A user file with the same name as a
// built-in overrides it.
func Find(k Kind, name string) (File, error) {
	// Fast path: the conventional file name.
	p := filepath.Join(UserDir(k), name+".toml")
	if data, err := os.ReadFile(p); err == nil && (nameOf(data) == name || nameOf(data) == "") {
		_, berr := Builtin(k, name)
		return File{Name: name, Path: p, Data: data, Override: berr == nil}, nil
	}
	for _, f := range userFiles(k) {
		if f.Name == name {
			_, berr := Builtin(k, name)
			f.Override = berr == nil
			return f, nil
		}
	}
	f, err := Builtin(k, name)
	if err != nil {
		return File{}, fmt.Errorf("no %s named %q (see `magenta %s list`)", strings.TrimSuffix(string(k), "s"), name, strings.TrimSuffix(string(k), "s"))
	}
	return f, nil
}

// List returns every file of this kind after overrides, sorted by name.
func List(k Kind) ([]File, error) {
	byName := map[string]File{}
	for _, n := range BuiltinNames(k) {
		f, err := Builtin(k, n)
		if err != nil {
			return nil, err
		}
		byName[f.Name] = f
	}
	for _, f := range userFiles(k) {
		_, builtin := byName[f.Name]
		f.Override = builtin && byName[f.Name].Builtin
		byName[f.Name] = f
	}
	out := make([]File, 0, len(byName))
	for _, f := range byName {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
