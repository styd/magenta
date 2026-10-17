package search

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/styd/magenta/internal/buffer"
	"github.com/styd/magenta/internal/render"
)

// PreviewOptions configures the preview pane.
type PreviewOptions struct {
	// Exe is the mgnt binary that fzf runs to draw the pane (see PreviewHelper).
	Exe string
	// Color says whether the pane may use color and other attributes.
	Color bool
}

const gutterWidth = 2 // the marker and a space

const (
	dim   = "\x1b[2m"
	rev   = "\x1b[7m"
	bold  = "\x1b[1m"
	reset = render.Reset
	// PreviewSubcommand is the hidden mgnt subcommand fzf runs for each
	// focused line.
	PreviewSubcommand = "__preview"
	maxSockPath       = 100 // sun_path is 104 bytes on macOS, 108 on Linux
)

// previewServer answers preview requests from the helper out of the ring. It
// listens on a Unix socket inside a private (0700) per-process temp directory,
// which Close removes.
type previewServer struct {
	ring  *buffer.Ring
	exe   string
	color bool
	dir   string
	sock  string
	ln    net.Listener
	wg    sync.WaitGroup

	// The preview can be scrolled with the keys while it is open. The position
	// is kept here, as a number of lines from where the focused line would be
	// centered, and applies to one focused line at a time.
	mu      sync.Mutex
	shift   int
	lastSeq uint64
	// onOpen is told when the preview is opened (true) or closed (false).
	onOpen func(open bool)
}

func startPreview(ring *buffer.Ring, o *PreviewOptions) (*previewServer, error) {
	if o.Exe == "" {
		return nil, fmt.Errorf("the mgnt executable is unknown")
	}
	dir, err := os.MkdirTemp("", "mgnt-")
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "p")
	if len(sock) > maxSockPath { // a very long $TMPDIR
		os.RemoveAll(dir)
		if dir, err = os.MkdirTemp("/tmp", "mgnt-"); err != nil {
			return nil, err
		}
		sock = filepath.Join(dir, "p")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	p := &previewServer{ring: ring, exe: o.Exe, color: o.Color, dir: dir, sock: sock, ln: ln}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

// HelperCommand is the command that reaches this server, for fzf to run.
func (p *previewServer) HelperCommand() string {
	return quoteArg(p.exe, runtime.GOOS) + " " + PreviewSubcommand + " --sock " + quoteArg(p.sock, runtime.GOOS)
}

// Command is what fzf runs for the focused line, {1} is the hidden sequence number.
func (p *previewServer) Command() string { return p.HelperCommand() + " {1}" }

// Dir is the private directory holding the socket.
func (p *previewServer) Dir() string { return p.dir }

// Close stops the server and removes the socket directory.
func (p *previewServer) Close() {
	p.ln.Close()
	p.wg.Wait()
	os.RemoveAll(p.dir)
}

// quoteArg quotes one word of a command line that fzf hands to a shell: sh on
// Unix, cmd.exe on Windows. cmd.exe does not know single quotes, and a path
// there cannot hold a double quote, so double quotes are enough.
func quoteArg(s, goos string) string {
	if goos == "windows" {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (p *previewServer) serve() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second))
			line, err := bufio.NewReader(io.LimitReader(c, 256)).ReadString('\n')
			if err != nil {
				return
			}
			c.Write(p.handle(strings.Fields(line)))
		}()
	}
}

// handle answers one request: "seq rows cols" draws the pane for a line, and
// "scroll N" / "scroll reset" moves the pane.
func (p *previewServer) handle(f []string) []byte {
	if len(f) == 1 && (f[0] == "open" || f[0] == "close") {
		p.mu.Lock()
		p.shift = 0 // it opens centered on the focused line, and starts so again
		cb := p.onOpen
		p.mu.Unlock()
		if cb != nil {
			cb(f[0] == "open")
		}
		return []byte("ok\n")
	}
	if len(f) == 2 && f[0] == "scroll" {
		p.mu.Lock()
		defer p.mu.Unlock()
		if f[1] == "reset" {
			p.shift = 0
		} else if n, err := strconv.Atoi(f[1]); err == nil {
			p.shift += n
		}
		return []byte("ok\n")
	}
	if len(f) != 3 {
		return nil
	}
	seq, e1 := strconv.ParseUint(f[0], 10, 64)
	rows, e2 := strconv.Atoi(f[1])
	cols, e3 := strconv.Atoi(f[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if seq != p.lastSeq { // another line is focused: start at its center
		p.shift, p.lastSeq = 0, seq
	}
	out, shift := renderShifted(p.ring, seq, rows, cols, p.color, p.shift)
	p.shift = shift // do not keep scrolling past an end that is already reached
	return out
}

// RenderWindow draws the preview pane for the line with this sequence number:
// as many lines around it as fill rows terminal rows of cols columns (a line
// that wraps takes several rows), with the focused line roughly in the middle,
// clamped at the ends of the buffer. The focused one is marked with a reversed
// arrow in a two column gutter. Lines carry no numbers. Lines that arrive later
// do not move the window, because the window is only drawn when a line is
// focused.
func RenderWindow(ring *buffer.Ring, seq uint64, rows, cols int, color bool) []byte {
	out, _ := renderShifted(ring, seq, rows, cols, color, 0)
	return out
}

// renderShifted is RenderWindow with the window moved shift lines from where it
// would be centered on the focused line (negative: up). It returns the shift
// that was actually applied, which is smaller when an end of the buffer was
// reached. The focused line is only marked while it is on screen.
func renderShifted(ring *buffer.Ring, seq uint64, rows, cols int, color bool, shift int) ([]byte, int) {
	style := func(codes, s string) string {
		if !color {
			return s
		}
		return codes + s + reset
	}
	if rows < 1 {
		rows = 24
	}
	if cols < 20 {
		cols = 80
	}
	textCols := max(cols-gutterWidth, 1)
	rowsOf := func(b []byte) int {
		w := render.VisibleWidth(b)
		return max((w+textCols-1)/textCols, 1)
	}
	lines, focus := ring.Window(seq, rows, rowsOf)
	if lines == nil {
		return []byte(style(dim, "[mgnt] that line is no longer in the buffer") + "\n"), 0
	}
	applied := 0
	if shift != 0 {
		oldest, _ := ring.Bounds()
		centered := int64(lines[0].Seq)
		start := centered + int64(shift)
		start = min(start, int64(ring.MaxStart(rows, rowsOf)))
		start = max(start, int64(oldest))
		applied = int(start - centered)
		lines = ring.From(uint64(start), rows, rowsOf)
		focus = -1
		for i, l := range lines {
			if l.Seq == seq {
				focus = i
			}
		}
	}
	var out bytes.Buffer
	for i, l := range lines {
		if i == focus {
			// A reversed arrow marks the line the cursor is on. There are no line
			// numbers: in a log that streams, a number says nothing to the reader.
			out.WriteString(style(rev+bold, "▶") + " ")
		} else {
			out.WriteString("  ")
		}
		out.Write(l.Text)
		if color {
			out.WriteString(reset)
		}
		out.WriteString("\n")
	}
	return out.Bytes(), applied
}

// PreviewHelper is the hidden `mgnt __preview` command that fzf starts for
// each focused line. args are the arguments after the subcommand name:
// --sock PATH SEQ. It asks the running mgnt for the pane over the socket and
// copies the answer to stdout. It must be fast and quiet: whatever goes wrong
// it prints one dim line to stdout, writes nothing to stderr (which would
// scribble over fzf's screen) and returns 0.
func PreviewHelper(args []string, getenv func(string) string, stdout io.Writer) int {
	unavailable := func() int {
		msg := "[mgnt] preview unavailable"
		if render.DetectDepth(getenv) != render.None {
			msg = dim + msg + reset
		}
		fmt.Fprintln(stdout, msg)
		return 0
	}
	var sock, seq, key string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--sock" && i+1 < len(args):
			sock = args[i+1]
			i++
		case args[i] == "--key" && i+1 < len(args):
			key = args[i+1]
			i++
		case seq == "":
			seq = args[i]
		}
	}
	if key != "" {
		return keyHelper(sock, key, getenv, stdout)
	}
	if sock == "" || seq == "" {
		return unavailable()
	}
	rows, cols := getenv("FZF_PREVIEW_LINES"), getenv("FZF_PREVIEW_COLUMNS")
	if rows == "" {
		rows = "0"
	}
	if cols == "" {
		cols = "0"
	}
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return unavailable()
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(c, "%s %s %s\n", seq, rows, cols); err != nil {
		return unavailable()
	}
	var resp bytes.Buffer
	if _, err := io.Copy(&resp, c); err != nil || resp.Len() == 0 {
		return unavailable()
	}
	stdout.Write(resp.Bytes())
	return 0
}

// request sends one line to the preview server and reads its answer.
func request(sock, line string) ([]byte, bool) {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintln(c, line); err != nil {
		return nil, false
	}
	var resp bytes.Buffer
	if _, err := io.Copy(&resp, c); err != nil {
		return nil, false
	}
	return resp.Bytes(), true
}

// wheelLines is how far one notch of the mouse wheel scrolls the preview.
const wheelLines = 3

// keyHelper is `mgnt __preview --key NAME`, which fzf runs (through its
// transform action) when one of a few keys is pressed, and which answers with
// the fzf action to perform. Whether the preview is open comes from fzf, which
// sets $FZF_PREVIEW_LABEL while the preview has its label.
//
// Closed: Enter opens it, Esc leaves fzf, and the movement keys move the list
// as usual. Open: Up, Down, Page Up, and Page Down scroll the preview, whatever
// the list is doing, and the list holds still: mgnt stops sending it new
// lines. Esc closes the preview and lets the list go on. The scroll position lives in the mgnt that
// serves the preview. It prints one action and nothing else, and does not fail.
func keyHelper(sock, key string, getenv func(string) string, stdout io.Writer) int {
	open := getenv("FZF_PREVIEW_LABEL") != ""
	rows := 10
	if n, err := strconv.Atoi(getenv("FZF_PREVIEW_LINES")); err == nil && n > 0 {
		rows = n
	}
	say := func(action string) int {
		fmt.Fprintln(stdout, action)
		return 0
	}
	// Opening freezes the list and starts at the focused line. Closing lets the
	// list go on. Both also put the scroll position back.
	opened := func() { request(sock, "open") }
	closed := func() { request(sock, "close") }
	switch key {
	case "esc":
		if !open {
			return say("abort")
		}
		closed()
		return say(hideAction)
	case "enter":
		if open {
			return say("ignore")
		}
		opened()
		return say(showAction)
	case "toggle":
		if open {
			closed()
			return say(hideAction)
		}
		opened()
		return say(showAction)
	case "wheel-up", "wheel-down":
		// The wheel only reaches a preview that is on screen.
		if !open {
			return say("ignore")
		}
		delta := map[string]int{"wheel-up": -wheelLines, "wheel-down": wheelLines}[key]
		if _, ok := request(sock, fmt.Sprintf("scroll %d", delta)); !ok {
			return say("ignore")
		}
		return say("refresh-preview")
	case "up", "down", "page-up", "page-down":
		if !open {
			return say(key) // the list moves as usual
		}
		delta := map[string]int{"up": -1, "down": 1, "page-up": -rows, "page-down": rows}[key]
		if _, ok := request(sock, fmt.Sprintf("scroll %d", delta)); !ok {
			return say("ignore")
		}
		return say("refresh-preview")
	}
	return say("ignore")
}
