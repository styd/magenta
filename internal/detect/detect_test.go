package detect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/styd/magenta/internal/config"
	"github.com/styd/magenta/internal/lines"
	"github.com/styd/magenta/internal/rules"
)

func builtinDetectors(t testing.TB) []*rules.Detector {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dets, errs := rules.ConfigLoader{}.Detectors()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return dets
}

func readLines(t testing.TB, name string, n int) [][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name, "in.log"))
	if err != nil {
		t.Fatal(err)
	}
	ls := lines.Split(data)
	if len(ls) > n {
		ls = ls[:n]
	}
	return ls
}

func TestEveryBuiltinIsDetectedFromItsOwnLog(t *testing.T) {
	dets := builtinDetectors(t)
	for _, name := range config.BuiltinNames(config.Rules) {
		t.Run(name, func(t *testing.T) {
			res := Choose(dets, readLines(t, name, SampleLines), "")
			if res.Picked != name {
				t.Errorf("picked %q, want %q, scores %+v", res.Picked, name, res.Scores)
			}
		})
	}
}

func mk(t *testing.T, name string, filenames []string, patterns ...string) *rules.Detector {
	t.Helper()
	d := &rules.Detector{Name: name, Filenames: filenames}
	for _, p := range patterns {
		src := "name = \"x\"\ndetect = ['" + p + "']\n"
		pf, err := rules.Parse("t.toml", []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		pd, err := pf.Detector()
		if err != nil {
			t.Fatal(err)
		}
		d.Patterns = append(d.Patterns, pd.Patterns...)
	}
	return d
}

func TestChooseTable(t *testing.T) {
	a := mk(t, "alpha", []string{"alpha*.log"}, `^A `)
	b := mk(t, "beta", []string{"beta.log"}, `^B `, `^b `)
	tests := []struct {
		name     string
		lines    []string
		filename string
		want     string
		total    map[string]int
	}{
		{"best by lines", []string{"A 1", "A 2", "B 3"}, "", "alpha", map[string]int{"alpha": 2, "beta": 1}},
		{"nothing matches falls back", []string{"x", "y", "z", "w"}, "", Fallback, nil},
		{"below threshold with many lines", []string{"A 1", "x", "y", "z", "w"}, "", Fallback, nil},
		{"one line is enough for a short sample", []string{"B 1"}, "", "beta", nil},
		{"two patterns on one line count once", []string{"B 1 b"}, "", "beta", map[string]int{"beta": 1}},
		{"filename bonus beats a weaker content match", []string{"A 1", "A 2", "x"}, "/var/log/beta.log", "beta", map[string]int{"beta": FilenameBonus}},
		{"filename bonus with no content match", []string{"x", "y", "z", "w", "v"}, "alpha1.log", "alpha", map[string]int{"alpha": FilenameBonus}},
		{"strong content beats the bonus", func() []string {
			var l []string
			for i := 0; i < FilenameBonus+3; i++ {
				l = append(l, "A x")
			}
			return l
		}(), "beta.log", "alpha", nil},
		{"unrelated filename gives no bonus", []string{"x", "y", "z", "w"}, "notes.txt", Fallback, nil},
		{"tie goes to the name that sorts first", []string{"A 1", "B 2"}, "", "alpha", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ls [][]byte
			for _, l := range tt.lines {
				ls = append(ls, []byte(l))
			}
			res := Choose([]*rules.Detector{b, a}, ls, tt.filename)
			if res.Picked != tt.want {
				t.Errorf("picked %q, want %q (%+v)", res.Picked, tt.want, res.Scores)
			}
			for _, s := range res.Scores {
				if want, ok := tt.total[s.Name]; ok && s.Total != want {
					t.Errorf("%s total %d, want %d", s.Name, s.Total, want)
				}
			}
		})
	}
}

func TestScoreReportsWhatMatched(t *testing.T) {
	d := mk(t, "beta", []string{"beta*.log"}, `^B `, `^zzz`, `3$`)
	res := Choose([]*rules.Detector{d}, [][]byte{[]byte("B 3"), []byte("B 4")}, "x/beta1.log")
	s := res.Scores[0]
	if s.Glob != "beta*.log" || s.Bonus != FilenameBonus || s.Lines != 2 || len(s.Patterns) != 2 ||
		s.Patterns[0].Src != `^B ` || s.Patterns[0].Lines != 2 || s.Patterns[1].Src != `3$` || s.Patterns[1].Lines != 1 {
		t.Errorf("%+v", s)
	}
}

func TestThreshold(t *testing.T) {
	for n, want := range map[int]int{0: 1, 1: 1, 3: 1, 4: 2, 20: 2} {
		if got := Threshold(n); got != want {
			t.Errorf("Threshold(%d) = %d, want %d", n, got, want)
		}
	}
}
