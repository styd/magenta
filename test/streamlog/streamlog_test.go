package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/styd/magenta/internal/detect"
	"github.com/styd/magenta/internal/rules"
)

var start = time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)

// lines makes the boot of a server and n events after it.
func lines(seed int64, n int) []entry {
	g := newGenerator(seed, start, 4)
	out := g.start()
	for range n {
		out = append(out, g.next(300*time.Millisecond)...)
	}
	return out
}

func TestLinesLookLikeALoggerLog(t *testing.T) {
	prefix := regexp.MustCompile(`^[IWEFDA], \[\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6} #\d+\]\s+(DEBUG|INFO|WARN|ERROR|FATAL) -- : .`)
	levels := map[byte]string{'D': "DEBUG", 'I': "INFO", 'W': "WARN", 'E': "ERROR", 'F': "FATAL"}
	sawRaw := false
	for _, e := range lines(1, 3000) {
		if strings.ContainsAny(e.text, "\r\n") {
			t.Fatalf("a line holds a line break: %q", e.text)
		}
		m := prefix.FindStringSubmatch(e.text)
		if m == nil {
			sawRaw = true // what the application writes to stderr itself
			continue
		}
		if levels[e.text[0]] != m[1] {
			t.Errorf("the letter and the name of the level differ: %q", e.text)
		}
	}
	if !sawRaw {
		t.Error("no line without a logger prefix in 3000 events")
	}
}

func TestTimesNeverGoBack(t *testing.T) {
	prev := start
	for _, e := range lines(2, 3000) {
		if e.at.Before(prev) {
			t.Fatalf("%q is at %v, before %v", e.text, e.at, prev)
		}
		prev = e.at
	}
}

func TestSameSeedSameLog(t *testing.T) {
	a, b, c := lines(3, 200), lines(3, 200), lines(4, 200)
	if len(a) != len(b) {
		t.Fatalf("%d and %d lines for one seed", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("line %d differs for one seed: %q and %q", i, a[i].text, b[i].text)
		}
	}
	if a[0] == c[0] && a[len(a)-1] == c[len(c)-1] {
		t.Error("two seeds gave the same log")
	}
}

// Workers are replaced with new pids, and a worker that was reaped never
// logs again.
func TestWorkersKeepToTheirPids(t *testing.T) {
	reaped := map[string]bool{}
	reap := regexp.MustCompile(`^I, \[[^ ]+ #\d+\]  INFO -- : reaped #<Process::Status: pid (\d+) `)
	pidOf := regexp.MustCompile(` #(\d+)\]`)
	for _, e := range lines(5, 3000) {
		if m := reap.FindStringSubmatch(e.text); m != nil {
			reaped[m[1]] = true
			continue
		}
		if m := pidOf.FindStringSubmatch(e.text); m != nil && reaped[m[1]] {
			t.Fatalf("a reaped process logs again: %q", e.text)
		}
	}
	if len(reaped) == 0 {
		t.Error("no worker was ever replaced in 3000 events")
	}
}

// Most of what a healthy application logs is requests that went well, and a
// failure is the exception.
func TestMostRequestsSucceed(t *testing.T) {
	completed := regexp.MustCompile(`Completed (\d{3}) `)
	status := map[string]int{}
	total, errorLines := 0, 0
	all := lines(9, 3000)
	for _, e := range all {
		if m := completed.FindStringSubmatch(e.text); m != nil {
			status[m[1]]++
			total++
		}
		if strings.HasPrefix(e.text, "E, ") || strings.HasPrefix(e.text, "    from ") {
			errorLines++
		}
	}
	if share := float64(status["200"]) / float64(total); share < 0.7 {
		t.Errorf("%.0f%% of the requests are 200, %v", 100*share, status)
	}
	for _, code := range []string{"201", "302", "304", "404", "422", "500"} {
		if status[code] == 0 {
			t.Errorf("no request ended with %s in 3000 events, %v", code, status)
		}
	}
	if share := float64(errorLines) / float64(len(all)); share > 0.25 {
		t.Errorf("%.0f%% of the lines are errors", 100*share)
	}
}

// Rails writes Started, Processing and Completed for a request. The
// exception comes after the Completed line: one line with the logger prefix,
// then the rest of the backtrace as "from" lines, down to the unicorn worker.
func TestRequestsAndBacktraces(t *testing.T) {
	raised := regexp.MustCompile("^E, .* ERROR -- : /\\S+:\\d+:in `[^']+': .+ \\(\\S+\\)$")
	from := regexp.MustCompile("^    from /\\S+:\\d+:in `[^']+'$")
	var state string
	var backtrace []string
	endOfError := func() {
		if backtrace == nil {
			return
		}
		joined := strings.Join(backtrace, "\n")
		for _, want := range []string{"_controller.rb:", "http_server.rb:634:in `process_client'", "bin/unicorn:25:in `<main>'"} {
			if !strings.Contains(joined, want) {
				t.Errorf("a backtrace lacks %q:\n%s", want, joined)
			}
		}
		backtrace = nil
	}
	errors := 0
	for _, e := range lines(10, 3000) {
		_, msg, _ := strings.Cut(e.text, " -- : ")
		switch {
		case raised.MatchString(e.text):
			endOfError()
			errors++
			backtrace = []string{msg}
		case strings.HasPrefix(e.text, "    from "):
			if backtrace == nil {
				t.Fatalf("a from line outside of a backtrace: %q", e.text)
			}
			if !from.MatchString(e.text) {
				t.Errorf("a line of a backtrace that is not a frame: %q", e.text)
			}
			backtrace = append(backtrace, e.text)
		default:
			endOfError()
			if e.text[0] == 'E' {
				t.Errorf("an error line that does not start a backtrace: %q", e.text)
			}
			switch {
			case strings.HasPrefix(msg, "Started "):
				if state != "" {
					t.Fatalf("a request starts in the middle of another: %q", e.text)
				}
				state = "started"
			case strings.HasPrefix(msg, "Processing by "):
				if state != "started" {
					t.Fatalf("Processing without Started: %q", e.text)
				}
			case strings.HasPrefix(msg, "Completed "):
				if state != "started" {
					t.Fatalf("Completed without Started: %q", e.text)
				}
				if e.text[0] != 'I' {
					t.Errorf("Rails logs the end of a request as INFO, whatever the status: %q", e.text)
				}
				state = ""
			}
		}
	}
	endOfError()
	if errors == 0 {
		t.Error("no request failed in 3000 events")
	}
}

// The log is meant to be tried with mgnt, so mgnt must take it for a unicorn
// log from its first lines, and the rule must have something to color in it.
func TestMgntDetectsItAsUnicorn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dets, errs := rules.ConfigLoader{}.Detectors()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var first [][]byte
	for _, e := range lines(6, 50)[:detect.SampleLines] {
		first = append(first, []byte(e.text))
	}
	if res := detect.Choose(dets, first, ""); res.Picked != "unicorn" {
		t.Errorf("picked %q, scores %+v", res.Picked, res.Scores)
	}
}

func TestStreamStopsAfterCount(t *testing.T) {
	var out bytes.Buffer
	g := newGenerator(7, time.Now(), 2)
	if err := stream(context.Background(), g, &out, time.Millisecond, 12); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "\n"); n != 12 {
		t.Errorf("%d lines, want 12", n)
	}
}

func TestStreamStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := stream(ctx, newGenerator(8, time.Now(), 2), &out, time.Millisecond, 0); err != nil || out.Len() != 0 {
		t.Errorf("err %v, wrote %q", err, out.String())
	}
}

func TestRunAppendsToAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "unicorn.log")
	if err := os.WriteFile(file, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := run([]string{"-interval", "1ms", "-lines", "5", file}, nil, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(got), "old line\n") || strings.Count(string(got), "\n") != 6 {
		t.Errorf("the file is %q", got)
	}
	if code := run([]string{"-interval", "1ms", "-lines", "5", "-truncate", file}, nil, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	got, _ = os.ReadFile(file)
	if strings.Contains(string(got), "old line") || strings.Count(string(got), "\n") != 5 {
		t.Errorf("after -truncate the file is %q", got)
	}
}

func TestRunWritesToStdoutForADash(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-interval", "1ms", "-lines", "3", "-"}, &stdout, &stderr); code != 0 || strings.Count(stdout.String(), "\n") != 3 {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestRunRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}, {"-workers", "0", "x"}, {"-interval", "0s", "x"}, {"-lines", "-1", "x"}, {"-nope", "x"}} {
		var stderr bytes.Buffer
		if code := run(args, nil, &stderr); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	if code := run([]string{"/no/such/dir/unicorn.log"}, nil, &bytes.Buffer{}); code != 1 {
		t.Errorf("an unwritable file: exit %d", code)
	}
	if code := run([]string{"-h"}, nil, &bytes.Buffer{}); code != 0 {
		t.Errorf("-h: exit %d", code)
	}
}
