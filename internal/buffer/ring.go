// Package buffer keeps the most recent colored lines, each with a global
// sequence number, so they can be searched and looked up again.
package buffer

import "sync"

// Line is one stored line.
type Line struct {
	Seq  uint64
	Text []byte
}

// Ring is a bounded buffer of lines. Sequence numbers start at 1, increase by
// one per line and are never reused, so a number identifies a line for the
// whole run, even after the line has been evicted. It is safe for concurrent
// use.
type Ring struct {
	mu      sync.RWMutex
	slots   []Line
	next    uint64 // sequence number the next line gets
	closed  bool
	changed chan struct{}
}

// New returns a ring that holds up to capacity lines.
func New(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{slots: make([]Line, capacity), next: 1, changed: make(chan struct{})}
}

func (r *Ring) notify() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// Append stores a copy of text and returns its sequence number.
func (r *Ring) Append(text []byte) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := r.next
	r.next++
	s := &r.slots[(seq-1)%uint64(len(r.slots))]
	s.Seq = seq
	s.Text = append(s.Text[:0], text...)
	r.notify()
	return seq
}

// Close says that no more lines will come.
func (r *Ring) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.notify()
	}
}

// Closed reports whether Close was called.
func (r *Ring) Closed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.closed
}

// Changed returns a channel that is closed the next time a line is appended
// or the ring is closed. Fetch it before looking at the ring, so that no
// change is missed.
func (r *Ring) Changed() <-chan struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.changed
}

// Bounds returns the sequence numbers of the oldest and newest stored lines,
// or 0, 0 when the ring is empty.
func (r *Ring) Bounds() (oldest, newest uint64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bounds()
}

func (r *Ring) bounds() (oldest, newest uint64) {
	if r.next == 1 {
		return 0, 0
	}
	newest = r.next - 1
	oldest = 1
	if c := uint64(len(r.slots)); newest > c {
		oldest = newest - c + 1
	}
	return oldest, newest
}

// Capacity is the number of lines the ring can hold.
func (r *Ring) Capacity() int { return len(r.slots) }

// Next returns the sequence number the next appended line will get.
func (r *Ring) Next() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.next
}

// Get returns a copy of the line with this sequence number, or false if it
// was never stored or has been evicted.
func (r *Ring) Get(seq uint64) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	oldest, newest := r.bounds()
	if seq == 0 || seq < oldest || seq > newest {
		return nil, false
	}
	return append([]byte(nil), r.slots[(seq-1)%uint64(len(r.slots))].Text...), true
}

// Collect returns copies of up to max lines starting at sequence number from
// (or at the oldest stored line, if from has been evicted), oldest first.
func (r *Ring) Collect(from uint64, max int) []Line {
	r.mu.RLock()
	defer r.mu.RUnlock()
	oldest, newest := r.bounds()
	if oldest == 0 || from > newest {
		return nil
	}
	if from < oldest {
		from = oldest
	}
	n := int(newest-from) + 1
	if n > max {
		n = max
	}
	out := make([]Line, 0, n)
	for seq := from; seq < from+uint64(n); seq++ {
		s := r.slots[(seq-1)%uint64(len(r.slots))]
		out = append(out, Line{seq, append([]byte(nil), s.Text...)})
	}
	return out
}

// Window returns the lines around seq that fit in rows terminal rows, where
// rowsOf says how many rows a line needs. The line itself comes first in the
// order of preference and the window is centered on it, then shifted so that
// it fills the available rows near either end of the buffer. It returns
// nil, 0 when seq is no longer (or not yet) stored. focus is the index of seq
// within the returned lines.
func (r *Ring) Window(seq uint64, rows int, rowsOf func([]byte) int) (lines []Line, focus int) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	oldest, newest := r.bounds()
	if seq == 0 || seq < oldest || seq > newest {
		return nil, 0
	}
	slot := func(s uint64) []byte { return r.slots[(s-1)%uint64(len(r.slots))].Text }
	cost := func(s uint64) int { return max(1, rowsOf(slot(s))) }

	lo, hi := seq, seq // the window is [lo, hi]
	used := cost(seq)
	// Grow alternately upwards and downwards, a line at a time, so the
	// focused line ends up roughly in the middle.
	for used < rows {
		grew := false
		if lo > oldest && used+cost(lo-1) <= rows {
			lo--
			used += cost(lo)
			grew = true
		}
		if used < rows && hi < newest && used+cost(hi+1) <= rows {
			hi++
			used += cost(hi)
			grew = true
		}
		if !grew {
			break
		}
	}
	for s := lo; s <= hi; s++ {
		lines = append(lines, Line{s, append([]byte(nil), slot(s)...)})
	}
	return lines, int(seq - lo)
}

// From returns the lines that fit in rows terminal rows starting at sequence
// number start (clamped to the oldest stored line), where rowsOf says how many
// rows a line needs. At least one line is returned if any is stored.
func (r *Ring) From(start uint64, rows int, rowsOf func([]byte) int) []Line {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.from(start, rows, rowsOf)
}

func (r *Ring) from(start uint64, rows int, rowsOf func([]byte) int) []Line {
	oldest, newest := r.bounds()
	if oldest == 0 {
		return nil
	}
	start = max(start, oldest)
	var out []Line
	used := 0
	for seq := start; seq <= newest; seq++ {
		text := r.slots[(seq-1)%uint64(len(r.slots))].Text
		cost := max(1, rowsOf(text))
		if used+cost > rows && used > 0 {
			break
		}
		used += cost
		out = append(out, Line{seq, append([]byte(nil), text...)})
	}
	return out
}

// MaxStart is the highest start for which From still fills rows rows, that is,
// the point where scrolling down stops with the newest line at the bottom. It
// is 0 when the ring is empty.
func (r *Ring) MaxStart(rows int, rowsOf func([]byte) int) uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	oldest, newest := r.bounds()
	if oldest == 0 {
		return 0
	}
	used, start := 0, newest+1
	for seq := newest; seq >= oldest; seq-- {
		cost := max(1, rowsOf(r.slots[(seq-1)%uint64(len(r.slots))].Text))
		if used+cost > rows && used > 0 {
			break
		}
		used += cost
		start = seq
	}
	return start
}
