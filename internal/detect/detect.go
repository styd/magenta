// Package detect chooses which rule file fits a log.
package detect

import (
	"sort"
	"time"

	"github.com/styd/magenta/internal/rules"
)

const (
	// SampleLines is how many lines are looked at before choosing.
	SampleLines = 10
	// SampleWait is the longest mgnt waits for those lines, so that tail -f
	// is not held back.
	SampleWait = 50 * time.Millisecond
	// FilenameBonus is added to a rule whose filenames glob matches the file
	// name. It is a strong hint: with it, a rule wins unless another rule
	// matches more than FilenameBonus of the sampled lines.
	FilenameBonus = 10
	// RedetectAfter is how many consecutive lines the locked-on rule may fail
	// to match before the choice is looked at again.
	RedetectAfter = 100
	// Fallback is the rule used when nothing scores above the threshold.
	Fallback = "generic"
)

// PatternHit says how many lines one detect pattern matched.
type PatternHit struct {
	Src   string
	Lines int
}

// Score is how well one rule file fits the sampled lines.
type Score struct {
	Name     string
	Lines    int // sampled lines matched by at least one detect pattern
	Bonus    int // FilenameBonus when a filenames glob matched
	Total    int
	Patterns []PatternHit
	Glob     string // the filenames glob that matched, if any
}

// Result is the outcome of a detection.
type Result struct {
	Scores    []Score // best first
	Picked    string  // rule name, Fallback when nothing qualified
	Threshold int
	Sampled   int
}

// Threshold is the lowest total a rule needs: two matching lines, or one when
// fewer than four lines were seen.
func Threshold(sampled int) int {
	if sampled < 4 {
		return 1
	}
	return 2
}

// Choose scores every detector against the lines (which must not contain
// ANSI escapes) and picks the best one above the threshold. filename may be
// empty. mgnt and `magenta detect` both call this, so they always agree.
func Choose(dets []*rules.Detector, lines [][]byte, filename string) Result {
	res := Result{Sampled: len(lines), Threshold: Threshold(len(lines)), Picked: Fallback}
	for _, d := range dets {
		s := Score{Name: d.Name}
		hits := make([]int, len(d.Patterns))
		for _, l := range lines {
			matched := false
			for i, p := range d.Patterns {
				if p.Re.Match(l) {
					hits[i]++
					matched = true
				}
			}
			if matched {
				s.Lines++
			}
		}
		for i, p := range d.Patterns {
			if hits[i] > 0 {
				s.Patterns = append(s.Patterns, PatternHit{p.Src, hits[i]})
			}
		}
		if filename != "" {
			if g, ok := d.MatchFilename(filename); ok {
				s.Glob, s.Bonus = g, FilenameBonus
			}
		}
		s.Total = s.Lines + s.Bonus
		res.Scores = append(res.Scores, s)
	}
	sort.SliceStable(res.Scores, func(i, j int) bool {
		a, b := res.Scores[i], res.Scores[j]
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		if a.Lines != b.Lines {
			return a.Lines > b.Lines
		}
		return a.Name < b.Name
	})
	if len(res.Scores) > 0 && res.Scores[0].Total >= res.Threshold {
		res.Picked = res.Scores[0].Name
	}
	return res
}
