package search

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/styd/magenta/internal/buffer"
	"github.com/styd/magenta/internal/config"
	"github.com/styd/magenta/internal/render"
)

func ringOf(n int, capacity int) *buffer.Ring {
	r := buffer.New(capacity)
	for i := 1; i <= n; i++ {
		r.Append([]byte(fmt.Sprintf("line %d", i)))
	}
	return r
}

func rendered(r *buffer.Ring, seq uint64, rows, cols int) []string {
	return strings.Split(strings.TrimSuffix(string(RenderWindow(r, seq, rows, cols, false)), "\n"), "\n")
}

func TestRenderWindowCentersOnTheFocusedLine(t *testing.T) {
	r := ringOf(100, 200)
	got := rendered(r, 50, 7, 80)
	// A two column gutter: the arrow on the focused line, blanks on the others. No numbers.
	want := []string{"  line 47", "  line 48", "  line 49", "▶ line 50", "  line 51", "  line 52", "  line 53"}
	if len(got) != 7 {
		t.Fatalf("%q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: %q, want %q", i, got[i], want[i])
		}
	}
	if !strings.HasPrefix(got[3], "▶") {
		t.Errorf("the focused line is not marked: %q", got[3])
	}
	for i, l := range got {
		if i != 3 && strings.Contains(l, "▶") {
			t.Errorf("row %d is marked too", i)
		}
	}
}

func TestRenderWindowAtTheEnds(t *testing.T) {
	r := ringOf(30, 100)
	first := rendered(r, 1, 5, 80)
	if len(first) != 5 || !strings.HasPrefix(first[0], "▶") || !strings.HasSuffix(first[4], "line 5") {
		t.Errorf("start of the buffer: %q", first)
	}
	last := rendered(r, 30, 5, 80)
	if len(last) != 5 || !strings.HasPrefix(last[4], "▶") || !strings.HasSuffix(last[0], "line 26") {
		t.Errorf("end of the buffer: %q", last)
	}
	small := rendered(ringOf(3, 10), 2, 20, 80)
	if len(small) != 3 {
		t.Errorf("a pane taller than the buffer shows the whole buffer: %q", small)
	}
}

func TestRenderWindowCountsWrappedRows(t *testing.T) {
	r := buffer.New(10)
	r.Append([]byte("short"))
	r.Append([]byte(strings.Repeat("w", 100))) // 100 columns of text
	r.Append([]byte("tail"))
	// 40 columns with a two column gutter leave 38 for the text: the long line needs 3 rows.
	got := rendered(r, 2, 5, 40)
	if len(got) != 3 {
		t.Fatalf("5 rows hold the 3-row focused line and one on each side: %q", got)
	}
	if got := rendered(r, 2, 3, 40); len(got) != 1 || !strings.HasPrefix(got[0], "▶") {
		t.Errorf("only the focused line fits in 3 rows: %q", got)
	}
}

func TestRenderWindowIgnoresColorCodesInWidth(t *testing.T) {
	r := buffer.New(10)
	colored := "\x1b[31m" + strings.Repeat("x", 10) + "\x1b[0m"
	for i := 0; i < 5; i++ {
		r.Append([]byte(colored))
	}
	// Ten visible columns fit in one row even though the bytes are longer than the pane is wide.
	if got := rendered(r, 3, 5, 20); len(got) != 5 {
		t.Errorf("escape sequences were counted as width: %d rows", len(got))
	}
	if out := RenderWindow(r, 3, 5, 20, true); !bytes.Contains(out, []byte("\x1b[31m")) {
		t.Error("the line's own colors must be kept")
	}
}

func TestRenderWindowEvictedAndStyling(t *testing.T) {
	r := ringOf(10, 3)
	if got := string(RenderWindow(r, 2, 5, 80, false)); got != "[mgnt] that line is no longer in the buffer\n" {
		t.Errorf("%q", got)
	}
	if got := string(RenderWindow(r, 2, 5, 80, true)); got != "\x1b[2m[mgnt] that line is no longer in the buffer\x1b[0m\n" {
		t.Errorf("%q", got)
	}
	// With color the focused arrow is reversed and bold, and the other gutters are blank.
	out := string(RenderWindow(r, 9, 3, 80, true))
	if !strings.Contains(out, "\x1b[7m\x1b[1m▶") && !strings.Contains(out, "\x1b[7;1m▶") && !strings.Contains(out, "\x1b[7m"+"\x1b[1m"+"▶") {
		t.Errorf("focused gutter is not highlighted: %q", out)
	}
	for _, row := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasPrefix(row, "\x1b[2m") {
			t.Errorf("a gutter is styled: %q", row)
		}
	}
	// Defaults for a missing size.
	if got := RenderWindow(ringOf(100, 100), 50, 0, 0, false); strings.Count(string(got), "\n") != 24 {
		t.Errorf("default rows: %d", strings.Count(string(got), "\n"))
	}
}

// shortTmp makes a short temporary directory and points the temporary
// directory variables at it, since a socket path has a small length limit.
func shortTmp(t *testing.T) string {
	t.Helper()
	parent := "/tmp"
	if runtime.GOOS == "windows" {
		parent = "" // there is no /tmp, and the default is short enough
	}
	d, err := os.MkdirTemp(parent, "pt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	t.Setenv("TMPDIR", d)
	t.Setenv("TMP", d) // Windows reads these two
	t.Setenv("TEMP", d)
	return d
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "mgnt-") {
			n = append(n, e.Name())
		}
	}
	return n
}

func TestServerAndHelper(t *testing.T) {
	tmp := shortTmp(t)
	ring := ringOf(60, 100)
	srv, err := startPreview(ring, &PreviewOptions{Exe: "/opt/my tools/mgnt's"})
	if err != nil {
		t.Fatal(err)
	}
	// The socket lives in a private directory.
	st, err := os.Stat(srv.Dir())
	if err != nil || filepath.Dir(srv.Dir()) != tmp {
		t.Fatalf("dir %v %v", st, err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 { // Windows has no permission bits
		t.Errorf("dir mode %v", st.Mode().Perm())
	}
	cmd := srv.Command()
	if !strings.HasPrefix(cmd, `'/opt/my tools/mgnt'\''s' __preview --sock '`) || !strings.HasSuffix(cmd, " {1}") {
		t.Errorf("command %q", cmd)
	}

	env := map[string]string{"FZF_PREVIEW_LINES": "9", "FZF_PREVIEW_COLUMNS": "60", "NO_COLOR": "1"}
	var out bytes.Buffer
	if code := PreviewHelper([]string{"--sock", srv.sock, "30"}, func(k string) string { return env[k] }, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := RenderWindow(ring, 30, 9, 60, false)
	if !bytes.Equal(out.Bytes(), want) {
		t.Errorf("helper printed\n%q\nwant\n%q", out.Bytes(), want)
	}
	if n := strings.Count(out.String(), "\n"); n != 9 {
		t.Errorf("%d rows for a 9 row pane", n)
	}

	// Later lines do not move a window that is drawn for the same line.
	for i := 0; i < 5; i++ {
		ring.Append([]byte("later"))
	}
	var again bytes.Buffer
	PreviewHelper([]string{"--sock", srv.sock, "30"}, func(k string) string { return env[k] }, &again)
	if !bytes.Equal(again.Bytes(), want) {
		t.Errorf("lines that arrived later moved the window:\n%q\nwant\n%q", again.Bytes(), want)
	}

	// An evicted line is reported, not an error.
	small := ringOf(10, 3)
	srv2, err := startPreview(small, &PreviewOptions{Exe: "mgnt"})
	if err != nil {
		t.Fatal(err)
	}
	var ev bytes.Buffer
	PreviewHelper([]string{"--sock", srv2.sock, "1"}, func(string) string { return "" }, &ev)
	if !strings.Contains(ev.String(), "that line is no longer in the buffer") {
		t.Errorf("%q", ev.String())
	}

	// Closing removes everything.
	sock := srv.sock
	srv.Close()
	srv2.Close()
	if left := entries(t, tmp); len(left) != 0 {
		t.Errorf("left behind in the temp dir: %v", left)
	}
	var gone bytes.Buffer
	if code := PreviewHelper([]string{"--sock", sock, "30"}, func(string) string { return "" }, &gone); code != 0 || !strings.Contains(gone.String(), "[mgnt] preview unavailable") {
		t.Errorf("after close: %d %q", code, gone.String())
	}
}

func TestHelperFailuresAreOneQuietLine(t *testing.T) {
	color := func(string) string { return "" }
	plain := func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}
	for _, args := range [][]string{
		{"--sock", "/nonexistent/socket", "3"},
		{"--sock", filepath.Join(os.TempDir(), "missing-sock"), "3"},
		{"3"},
		{"--sock", "/x"},
		{},
	} {
		var out bytes.Buffer
		if code := PreviewHelper(args, color, &out); code != 0 {
			t.Errorf("%v: exit %d", args, code)
		}
		if out.String() != "\x1b[2m[mgnt] preview unavailable\x1b[0m\n" {
			t.Errorf("%v: %q", args, out.String())
		}
	}
	var out bytes.Buffer
	PreviewHelper([]string{"--sock", "/nonexistent", "3"}, plain, &out)
	if out.String() != "[mgnt] preview unavailable\n" {
		t.Errorf("NO_COLOR: %q", out.String())
	}
}

func TestStartPreviewFailsCleanly(t *testing.T) {
	tmp := shortTmp(t)
	if _, err := startPreview(ringOf(1, 1), &PreviewOptions{}); err == nil {
		t.Error("no executable must be an error")
	}
	gone := filepath.Join(tmp, "does-not-exist")
	t.Setenv("TMPDIR", gone)
	t.Setenv("TMP", gone) // Windows reads these two
	t.Setenv("TEMP", gone)
	if _, err := startPreview(ringOf(1, 1), &PreviewOptions{Exe: "mgnt"}); err == nil {
		t.Error("an unusable temp dir must be an error")
	}
}

func TestVeryLongTmpdirFallsBackToTmp(t *testing.T) {
	base := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Skip("there is no /tmp to fall back to")
	}
	deep := base
	for len(deep) < 120 {
		deep = filepath.Join(deep, "a-rather-long-directory-name")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", deep)
	srv, err := startPreview(ringOf(3, 3), &PreviewOptions{Exe: "mgnt"})
	if err != nil {
		t.Fatalf("a long $TMPDIR broke the preview: %v", err)
	}
	defer srv.Close()
	if !strings.HasPrefix(srv.Dir(), "/tmp/") {
		t.Errorf("dir %s", srv.Dir())
	}
}

// fakeFzf exits at once with the status in $FAKE_EXIT, after saving its arguments.
const fakeFzfQuick = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_OUT"
exit ${FAKE_EXIT:-0}
`

func TestRunRemovesTheSocketDirectoryOnEveryPath(t *testing.T) {
	needShell(t)
	tmp := shortTmp(t)
	bin := t.TempDir()
	fzf := filepath.Join(bin, "fzf")
	os.WriteFile(fzf, []byte(fakeFzfQuick), 0o755)
	argsFile := filepath.Join(bin, "args")

	tests := []struct {
		name string
		exit string
		path string // fzf path to use
		want int
	}{
		{"selection", "0", fzf, 0},
		{"no match", "1", fzf, 1},
		{"canceled", "130", fzf, 130},
		{"error", "2", fzf, 2},
		{"fzf does not start", "0", filepath.Join(bin, "missing"), -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(Options{
				FzfPath: tt.path, Config: config.Default(), Ring: ringOf(3, 3),
				Environ: []string{"FAKE_EXIT=" + tt.exit, "FAKE_OUT=" + argsFile},
				Version: func(string) (string, error) { return "0.74.4 (test)", nil },
				Preview: &PreviewOptions{Exe: "/path/mgnt"},
			})
			res := s.Run("", NewestLast)
			if res.Code != tt.want {
				t.Errorf("code %d, want %d (%v)", res.Code, tt.want, res.Err)
			}
			if left := entries(t, tmp); len(left) != 0 {
				t.Errorf("socket directory left behind: %v", left)
			}
		})
	}

	// The preview flags reach fzf.
	s := New(Options{
		FzfPath: fzf, Config: config.Default(), Ring: ringOf(1, 1),
		Environ: []string{"FAKE_OUT=" + argsFile},
		Version: func(string) (string, error) { return "0.74.4 (test)", nil },
		Preview: &PreviewOptions{Exe: "/path/mgnt"},
	})
	s.Run("", NewestLast)
	data, _ := os.ReadFile(argsFile)
	got := string(data)
	for _, want := range []string{"--preview\n'/path/mgnt' __preview --sock '/tmp/", "{1}\n--preview-window\nup,50%,wrap,border-bottom,hidden\n--header\n", "\n--bind\nesc:transform:", "\n--bind\nenter:transform:'/path/mgnt' __preview --sock '/tmp/", "\n--bind\nctrl-o:accept\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q:\n%s", want, got)
		}
	}
}

func TestRunWithoutAPreviewExecutableStillSearches(t *testing.T) {
	needShell(t)
	shortTmp(t)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "fzf"), []byte(fakeFzfQuick), 0o755)
	argsFile := filepath.Join(bin, "args")
	s := New(Options{
		FzfPath: filepath.Join(bin, "fzf"), Config: config.Default(), Ring: ringOf(1, 1),
		Environ: []string{"FAKE_OUT=" + argsFile},
		Version: func(string) (string, error) { return "0.74.4", nil },
		Preview: &PreviewOptions{}, // exe unknown
	})
	res := s.Run("", NewestLast)
	data, _ := os.ReadFile(argsFile)
	if res.Code != 0 || strings.Contains(string(data), "--preview") {
		t.Errorf("code %d, args %s", res.Code, data)
	}
	if len(res.Notices) != 1 || !strings.Contains(res.Notices[0], "preview pane unavailable") {
		t.Errorf("notices %q", res.Notices)
	}
}

// ---- scrolling the open preview ----------------------------------------------

func rows(out []byte) []string {
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

func TestRenderShiftedMovesTheWindow(t *testing.T) {
	r := ringOf(100, 200)
	// Centered on line 50 in 7 rows: lines 47 to 53, focus on 50.
	out, shift := renderShifted(r, 50, 7, 80, false, 0)
	if shift != 0 || len(rows(out)) != 7 || !strings.HasPrefix(rows(out)[3], "▶ ") {
		t.Fatalf("%d %q", shift, out)
	}
	// Down 10 lines: 57 to 63, and the focused line is no longer on the screen, so no marker.
	out, shift = renderShifted(r, 50, 7, 80, false, 10)
	got := rows(out)
	if shift != 10 || !strings.HasSuffix(got[0], "line 57") || !strings.HasSuffix(got[6], "line 63") || strings.Contains(string(out), "▶") {
		t.Errorf("down: %d %q", shift, got)
	}
	// Up 4 lines: 43 to 49.
	out, shift = renderShifted(r, 50, 7, 80, false, -4)
	got = rows(out)
	if shift != -4 || !strings.HasSuffix(got[0], "line 43") || !strings.HasSuffix(got[6], "line 49") {
		t.Errorf("up: %d %q", shift, got)
	}
	// A shift that only moves the line, not off the screen, keeps the marker on it.
	out, _ = renderShifted(r, 50, 7, 80, false, 2)
	if got = rows(out); !strings.HasPrefix(got[1], "▶ ") && strings.HasSuffix(got[1], "line 50") {
		t.Errorf("marker should follow the line: %q", got)
	}
	// Scrolling is clamped at both ends, and the shift that was applied is reported.
	out, shift = renderShifted(r, 50, 7, 80, false, 1000)
	got = rows(out)
	if !strings.HasSuffix(got[6], "line 100") || len(got) != 7 || shift != 94-47 { // the last screen starts at 94
		t.Errorf("end: %d %q", shift, got)
	}
	out, shift = renderShifted(r, 50, 7, 80, false, -1000)
	got = rows(out)
	if !strings.HasSuffix(got[0], "line 1") || shift != 1-47 {
		t.Errorf("start: %d %q", shift, got)
	}
	// An evicted line has nothing to scroll around.
	if out, _ := renderShifted(ringOf(10, 3), 2, 5, 80, false, 3); !strings.Contains(string(out), "no longer in the buffer") {
		t.Errorf("%q", out)
	}
}

func TestServerScrollStateAndFocus(t *testing.T) {
	shortTmp(t)
	ring := ringOf(200, 300)
	srv, err := startPreview(ring, &PreviewOptions{Exe: "/opt/mgnt"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	env := map[string]string{"FZF_PREVIEW_LINES": "9", "FZF_PREVIEW_COLUMNS": "60", "NO_COLOR": "1"}
	get := func(k string) string { return env[k] }
	render := func(seq string) []string {
		var out bytes.Buffer
		PreviewHelper([]string{"--sock", srv.sock, seq}, get, &out)
		return rows(out.Bytes())
	}
	scroll := func(cmd string) {
		if _, ok := request(srv.sock, cmd); !ok {
			t.Fatalf("%s: no answer", cmd)
		}
	}
	// first is the number of the first line shown, read from its text ("line 96").
	first := func(r []string) string {
		f := strings.Fields(r[0])
		return f[len(f)-1]
	}

	if r := render("100"); first(r) != "96" { // centered: 96 to 104
		t.Fatalf("%q", r)
	}
	scroll("scroll 5")
	if r := render("100"); first(r) != "101" {
		t.Errorf("after scroll 5: %q", r)
	}
	scroll("scroll -2")
	if r := render("100"); first(r) != "99" {
		t.Errorf("after scroll -2: %q", r)
	}
	// Another focused line starts at its own center.
	if r := render("50"); first(r) != "46" {
		t.Errorf("another line: %q", r)
	}
	scroll("scroll 3")
	scroll("scroll reset")
	if r := render("50"); first(r) != "46" {
		t.Errorf("after reset: %q", r)
	}
	// Pressing past the end does not wind the position up: one step back moves at once.
	scroll("scroll 100000")
	if r := render("50"); !strings.HasSuffix(r[8], "line 200") {
		t.Fatalf("end: %q", r)
	}
	scroll("scroll -1")
	if r := render("50"); !strings.HasSuffix(r[8], "line 199") {
		t.Errorf("one step back from the end: %q", r)
	}
	// Bad requests are ignored, not fatal.
	for _, bad := range []string{"scroll", "scroll x", "nonsense", "1 2", "x y z"} {
		request(srv.sock, bad)
	}
	if r := render("50"); !strings.HasSuffix(r[8], "line 199") {
		t.Errorf("a bad request changed the state: %q", r)
	}
}

func TestKeyHelperAnswers(t *testing.T) {
	shortTmp(t)
	ring := ringOf(100, 200)
	srv, err := startPreview(ring, &PreviewOptions{Exe: "/opt/mgnt"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	answer := func(sock, key string, open bool, lines string) string {
		env := map[string]string{"FZF_PREVIEW_LINES": lines}
		if open {
			env["FZF_PREVIEW_LABEL"] = previewLabel
		}
		var out bytes.Buffer
		if code := PreviewHelper([]string{"--sock", sock, "--key", key}, func(k string) string { return env[k] }, &out); code != 0 {
			t.Errorf("%s: exit %d", key, code)
		}
		if n := strings.Count(out.String(), "\n"); n != 1 {
			t.Errorf("%s: %d lines of output, want exactly one action: %q", key, n, out.String())
		}
		return strings.TrimSpace(out.String())
	}
	dead := "/nonexistent/sock"
	tests := []struct {
		key        string
		open       bool
		sock       string
		want       string
		wantScroll int
	}{
		// Closed: the keys do what they always do, and Enter opens.
		{"esc", false, srv.sock, "abort", 0},
		{"enter", false, srv.sock, showAction, 0},
		{"up", false, srv.sock, "up", 0},
		{"down", false, srv.sock, "down", 0},
		{"page-up", false, srv.sock, "page-up", 0},
		{"page-down", false, srv.sock, "page-down", 0},
		{"toggle", false, srv.sock, showAction, 0},
		// Open: Esc closes, Enter does nothing more, the movement keys scroll the preview.
		{"esc", true, srv.sock, hideAction, 0},
		{"enter", true, srv.sock, "ignore", 0},
		{"toggle", true, srv.sock, hideAction, 0},
		{"down", true, srv.sock, "refresh-preview", 1},
		{"up", true, srv.sock, "refresh-preview", -1},
		{"page-down", true, srv.sock, "refresh-preview", 12},
		{"page-up", true, srv.sock, "refresh-preview", -12},
		// Unknown keys do nothing.
		{"f5", true, srv.sock, "ignore", 0},
		// The server is gone: the screen part still works, scrolling does nothing, navigation goes on.
		{"esc", true, dead, hideAction, 0},
		{"enter", false, dead, showAction, 0},
		{"down", true, dead, "ignore", 0},
		{"down", false, dead, "down", 0},
		{"esc", false, dead, "abort", 0},
	}
	for _, tt := range tests {
		request(srv.sock, "scroll reset")
		if got := answer(tt.sock, tt.key, tt.open, "12"); got != tt.want {
			t.Errorf("%s (open %v, socket %v): %q, want %q", tt.key, tt.open, tt.sock == srv.sock, got, tt.want)
		}
		if tt.sock == srv.sock {
			srv.mu.Lock()
			shift := srv.shift
			srv.mu.Unlock()
			if shift != tt.wantScroll {
				t.Errorf("%s (open %v): scrolled by %d, want %d", tt.key, tt.open, shift, tt.wantScroll)
			}
		}
	}
	// The page is the height of the pane, and a missing height still scrolls a little.
	request(srv.sock, "scroll reset")
	if got := answer(srv.sock, "page-down", true, ""); got != "refresh-preview" {
		t.Error(got)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.shift != 10 {
		t.Errorf("default page %d", srv.shift)
	}
}

// The whole round trip with a real server: Enter, scroll, render, Esc.
func TestScrollingThroughTheHelper(t *testing.T) {
	shortTmp(t)
	ring := ringOf(300, 400)
	srv, err := startPreview(ring, &PreviewOptions{Exe: "/opt/mgnt"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	run := func(open bool, args ...string) string {
		env := map[string]string{"FZF_PREVIEW_LINES": "8", "FZF_PREVIEW_COLUMNS": "60", "NO_COLOR": "1"}
		if open {
			env["FZF_PREVIEW_LABEL"] = previewLabel
		}
		var out bytes.Buffer
		PreviewHelper(append([]string{"--sock", srv.sock}, args...), func(k string) string { return env[k] }, &out)
		return out.String()
	}
	// top is the number of the first line shown, read from its text ("line 146").
	top := func() string {
		f := strings.Fields(strings.Split(run(true, "150"), "\n")[0])
		return f[len(f)-1]
	}

	run(false, "--key", "enter") // opens: scroll position reset
	if got := top(); got != "146" {
		t.Fatalf("centered on 150: %q", got)
	}
	for i := 0; i < 5; i++ {
		run(true, "--key", "down")
	}
	if got := top(); got != "151" {
		t.Errorf("five lines down: %q", got)
	}
	run(true, "--key", "page-up") // 8 lines up
	if got := top(); got != "143" {
		t.Errorf("a page up: %q", got)
	}
	run(true, "--key", "esc") // closes, and the next time it opens it is centered again
	run(false, "--key", "enter")
	if got := top(); got != "146" {
		t.Errorf("reopened: %q", got)
	}
	// Scrolling far above the focused line is possible: the whole buffer is there.
	for i := 0; i < 20; i++ {
		run(true, "--key", "page-up")
	}
	if got := top(); got != "1" {
		t.Errorf("the start of the buffer: %q", got)
	}
}

// Opening the preview tells the owner of the list to hold still, closing lets it go.
func TestOpenAndCloseReachTheFeed(t *testing.T) {
	shortTmp(t)
	srv, err := startPreview(ringOf(10, 20), &PreviewOptions{Exe: "/opt/mgnt"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	var calls []bool
	srv.onOpen = func(open bool) { calls = append(calls, open) }
	key := func(name string, open bool) string {
		env := map[string]string{"FZF_PREVIEW_LINES": "8"}
		if open {
			env["FZF_PREVIEW_LABEL"] = previewLabel
		}
		var out bytes.Buffer
		PreviewHelper([]string{"--sock", srv.sock, "--key", name}, func(k string) string { return env[k] }, &out)
		return strings.TrimSpace(out.String())
	}
	srv.shift = 7
	key("enter", false) // opens
	if len(calls) != 1 || !calls[0] || srv.shift != 0 {
		t.Errorf("enter: calls %v, shift %d", calls, srv.shift)
	}
	key("enter", true) // already open: nothing happens
	key("down", true)  // scrolling does not touch the list's feed
	if len(calls) != 1 {
		t.Errorf("calls %v", calls)
	}
	key("esc", true) // closes
	if len(calls) != 2 || calls[1] {
		t.Errorf("esc: calls %v", calls)
	}
	key("esc", false) // leaving fzf is not a preview event
	key("up", false)
	if len(calls) != 2 {
		t.Errorf("calls %v", calls)
	}
	key("toggle", false) // the toggle key opens and closes too
	key("toggle", true)
	if len(calls) != 4 || !calls[2] || calls[3] {
		t.Errorf("toggle: calls %v", calls)
	}
	// A server without anyone listening, and bad requests, are fine.
	srv.onOpen = nil
	if _, ok := request(srv.sock, "open"); !ok {
		t.Error("open without a listener")
	}
	srv.onOpen = func(bool) { t.Error("called for a bad request") }
	request(srv.sock, "opened")
	request(srv.sock, "open now")
}

func TestWheelOverThePreview(t *testing.T) {
	shortTmp(t)
	srv, err := startPreview(ringOf(100, 200), &PreviewOptions{Exe: "/opt/mgnt"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	key := func(name string, open bool) string {
		env := map[string]string{"FZF_PREVIEW_LINES": "12"}
		if open {
			env["FZF_PREVIEW_LABEL"] = previewLabel
		}
		var out bytes.Buffer
		PreviewHelper([]string{"--sock", srv.sock, "--key", name}, func(k string) string { return env[k] }, &out)
		return strings.TrimSpace(out.String())
	}
	srv.shift = 0
	if got := key("wheel-down", true); got != "refresh-preview" || srv.shift != wheelLines {
		t.Errorf("wheel down: %q, shift %d", got, srv.shift)
	}
	if got := key("wheel-up", true); got != "refresh-preview" || srv.shift != 0 {
		t.Errorf("wheel up: %q, shift %d", got, srv.shift)
	}
	// The wheel only reaches a preview that is on screen. Closed, it does nothing.
	if got := key("wheel-down", false); got != "ignore" || srv.shift != 0 {
		t.Errorf("closed: %q, shift %d", got, srv.shift)
	}
	// And the plan binds the events fzf sends for it.
	p := PlanFor(Version{0, 74, 4}, true, config.Default())
	var found int
	for _, b := range p.Binds {
		if strings.HasPrefix(b, "preview-scroll-up:") || strings.HasPrefix(b, "preview-scroll-down:") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("%d wheel bindings", found)
	}
}

// A log that streams has no meaningful line numbers, so the preview shows none:
// every row is a two column gutter followed by the line's own text.
func TestPreviewHasNoLineNumbers(t *testing.T) {
	r := ringOf(1000, 2000)
	for _, color := range []bool{false, true} {
		out := string(RenderWindow(r, 500, 9, 80, color))
		for _, row := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			plain := []rune(string(render.StripSGR(nil, []byte(row))))
			gutter, text := string(plain[:2]), string(plain[2:])
			if gutter != "▶ " && gutter != "  " {
				t.Errorf("unexpected gutter %q in %q", gutter, row)
			}
			if !strings.HasPrefix(text, "line ") {
				t.Errorf("the text does not start with the line itself: %q", text)
			}
		}
	}
}

func TestQuoteArg(t *testing.T) {
	tests := []struct{ goos, in, want string }{
		{"linux", "/tmp/mgnt", `'/tmp/mgnt'`},
		{"darwin", "it's here", `'it'\''s here'`},
		{"freebsd", "a b", `'a b'`},
		{"windows", `C:\Program Files\mgnt.exe`, `"C:\Program Files\mgnt.exe"`},
	}
	for _, tt := range tests {
		if got := quoteArg(tt.in, tt.goos); got != tt.want {
			t.Errorf("%s %q: got %s, want %s", tt.goos, tt.in, got, tt.want)
		}
	}
}
