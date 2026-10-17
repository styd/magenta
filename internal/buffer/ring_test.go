package buffer

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func fill(r *Ring, n int) {
	for i := 1; i <= n; i++ {
		r.Append([]byte(fmt.Sprintf("line %d", i)))
	}
}

func TestAppendGetAndEviction(t *testing.T) {
	r := New(3)
	if o, n := r.Bounds(); o != 0 || n != 0 {
		t.Errorf("empty bounds %d %d", o, n)
	}
	if _, ok := r.Get(1); ok {
		t.Error("Get on an empty ring")
	}
	fill(r, 2)
	if o, n := r.Bounds(); o != 1 || n != 2 {
		t.Errorf("bounds %d %d", o, n)
	}
	fill(r, 0)
	for i := 3; i <= 5; i++ {
		if seq := r.Append([]byte(fmt.Sprintf("line %d", i))); seq != uint64(i) {
			t.Errorf("seq %d, want %d", seq, i)
		}
	}
	// 5 lines in a ring of 3: 1 and 2 are gone, but their numbers are not reused.
	if o, n := r.Bounds(); o != 3 || n != 5 {
		t.Errorf("bounds %d %d", o, n)
	}
	for seq, want := range map[uint64]string{3: "line 3", 4: "line 4", 5: "line 5"} {
		if got, ok := r.Get(seq); !ok || string(got) != want {
			t.Errorf("Get(%d) = %q, %v", seq, got, ok)
		}
	}
	for _, seq := range []uint64{0, 1, 2, 6, 100} {
		if _, ok := r.Get(seq); ok {
			t.Errorf("Get(%d) should fail", seq)
		}
	}
	if r.Next() != 6 {
		t.Errorf("Next %d", r.Next())
	}
}

func TestAppendCopiesAndGetCopies(t *testing.T) {
	r := New(2)
	b := []byte("abc")
	r.Append(b)
	b[0] = 'X'
	got, _ := r.Get(1)
	if string(got) != "abc" {
		t.Errorf("Append kept the caller's slice: %q", got)
	}
	got[1] = 'Y'
	again, _ := r.Get(1)
	if string(again) != "abc" {
		t.Errorf("Get returned the internal slice: %q", again)
	}
}

func TestCollect(t *testing.T) {
	r := New(5)
	fill(r, 8) // holds 4..8
	texts := func(ls []Line) string {
		var s []string
		for _, l := range ls {
			s = append(s, fmt.Sprintf("%d:%s", l.Seq, l.Text))
		}
		return strings.Join(s, ",")
	}
	if got := texts(r.Collect(1, 100)); got != "4:line 4,5:line 5,6:line 6,7:line 7,8:line 8" {
		t.Errorf("from an evicted number starts at the oldest: %s", got)
	}
	if got := texts(r.Collect(6, 2)); got != "6:line 6,7:line 7" {
		t.Errorf("%s", got)
	}
	if got := r.Collect(9, 10); got != nil {
		t.Errorf("past the end: %v", got)
	}
	if got := New(3).Collect(1, 10); got != nil {
		t.Errorf("empty: %v", got)
	}
}

func TestWindow(t *testing.T) {
	one := func([]byte) int { return 1 }
	r := New(100)
	fill(r, 50)
	seqs := func(ls []Line) []uint64 {
		var out []uint64
		for _, l := range ls {
			out = append(out, l.Seq)
		}
		return out
	}
	tests := []struct {
		name      string
		seq       uint64
		rows      int
		wantFirst uint64
		wantLen   int
		wantFocus int
	}{
		{"centered", 25, 11, 20, 11, 5},
		{"centered, even rows: the extra row goes above", 25, 10, 20, 10, 5},
		{"start of buffer fills downwards", 1, 9, 1, 9, 0},
		{"near the start", 3, 9, 1, 9, 2},
		{"end of buffer fills upwards", 50, 9, 42, 9, 8},
		{"near the end", 48, 9, 42, 9, 6},
		{"one row", 25, 1, 25, 1, 0},
		{"pane taller than the buffer", 25, 80, 1, 50, 24},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls, focus := r.Window(tt.seq, tt.rows, one)
			if len(ls) != tt.wantLen || ls[0].Seq != tt.wantFirst || focus != tt.wantFocus || ls[focus].Seq != tt.seq {
				t.Errorf("got %v focus %d", seqs(ls), focus)
			}
			for i := 1; i < len(ls); i++ {
				if ls[i].Seq != ls[i-1].Seq+1 {
					t.Errorf("not consecutive: %v", seqs(ls))
				}
			}
		})
	}
}

func TestWindowCountsWrappedRows(t *testing.T) {
	r := New(10)
	r.Append([]byte("a"))
	r.Append([]byte("b"))
	r.Append([]byte(strings.Repeat("w", 25))) // 3 rows at 10 columns
	r.Append([]byte("d"))
	r.Append([]byte("e"))
	rowsOf := func(b []byte) int { return (len(b) + 9) / 10 }
	// 6 rows around line 3 (3 rows): lines 1-4 use 1+1+3+1 = 6 rows exactly.
	ls, focus := r.Window(3, 6, rowsOf)
	if len(ls) != 4 || ls[0].Seq != 1 || ls[focus].Seq != 3 || focus != 2 {
		t.Errorf("%v focus %d", ls, focus)
	}
	// 5 rows: one line each side of the three-row line fills them exactly.
	ls, focus = r.Window(3, 5, rowsOf)
	if len(ls) != 3 || ls[0].Seq != 2 || ls[focus].Seq != 3 {
		t.Errorf("%v focus %d", ls, focus)
	}
	// A focused line taller than the pane is returned alone.
	ls, focus = r.Window(3, 2, rowsOf)
	if len(ls) != 1 || ls[focus].Seq != 3 {
		t.Errorf("%v focus %d", ls, focus)
	}
}

func TestWindowEvictedOrFuture(t *testing.T) {
	r := New(3)
	fill(r, 10)
	for _, seq := range []uint64{0, 1, 7, 11} {
		if ls, _ := r.Window(seq, 5, func([]byte) int { return 1 }); ls != nil {
			t.Errorf("Window(%d) = %v", seq, ls)
		}
	}
}

func TestChangedAndClose(t *testing.T) {
	r := New(4)
	ch := r.Changed()
	select {
	case <-ch:
		t.Fatal("changed before anything happened")
	default:
	}
	r.Append([]byte("x"))
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("Append did not signal")
	}
	ch = r.Changed()
	if r.Closed() {
		t.Error("closed too early")
	}
	r.Close()
	r.Close() // twice is fine
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("Close did not signal")
	}
	if !r.Closed() {
		t.Error("not closed")
	}
}

func TestConcurrentUse(t *testing.T) {
	r := New(50)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		fill(r, 5000)
		r.Close()
	}()
	next := uint64(1)
	go func() {
		defer wg.Done()
		for {
			// The channel and the closed flag come before the lines, so a change
			// that happens after them is never missed. A batch that came back full
			// may have more behind it, so wait only when it came back empty.
			ch := r.Changed()
			closed := r.Closed()
			lines := r.Collect(next, 7)
			for _, l := range lines {
				if l.Seq < next {
					t.Errorf("went backwards: %d after %d", l.Seq, next)
				}
				next = l.Seq + 1
			}
			if len(lines) > 0 {
				continue
			}
			if closed {
				return
			}
			<-ch
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			r.Window(uint64(i%5000+1), 10, func([]byte) int { return 1 })
			r.Get(uint64(i + 1))
		}
	}()
	wg.Wait()
	// Slow readers lose old lines, but the newest one is always seen.
	if next != 5001 {
		t.Errorf("the reader ended before the last line: next is %d, want 5001", next)
	}
}

func TestFromAndMaxStart(t *testing.T) {
	r := New(100)
	fill(r, 30)
	one := func([]byte) int { return 1 }
	first := func(ls []Line) uint64 { return ls[0].Seq }
	last := func(ls []Line) uint64 { return ls[len(ls)-1].Seq }
	if ls := r.From(10, 5, one); len(ls) != 5 || first(ls) != 10 || last(ls) != 14 {
		t.Errorf("%v", ls)
	}
	// Before the oldest line it starts at the oldest. Past the end it shows what is left.
	if ls := r.From(0, 3, one); first(ls) != 1 || len(ls) != 3 {
		t.Errorf("%v", ls)
	}
	if ls := r.From(29, 5, one); first(ls) != 29 || last(ls) != 30 {
		t.Errorf("%v", ls)
	}
	if r.MaxStart(5, one) != 26 || r.MaxStart(100, one) != 1 {
		t.Errorf("max start %d %d", r.MaxStart(5, one), r.MaxStart(100, one))
	}
	// Wrapped lines count by the rows they take.
	r2 := New(10)
	r2.Append([]byte("a"))
	r2.Append([]byte(strings.Repeat("w", 25))) // 3 rows at 10 columns
	r2.Append([]byte("c"))
	rowsOf := func(b []byte) int { return (len(b) + 9) / 10 }
	if ls := r2.From(1, 4, rowsOf); len(ls) != 2 || last(ls) != 2 { // a (1) + w (3) = 4 rows, so c does not fit
		t.Errorf("%v", ls)
	}
	if got := r2.MaxStart(4, rowsOf); got != 2 { // c (1) + w (3)
		t.Errorf("max start %d", got)
	}
	if New(3).From(1, 5, one) != nil || New(3).MaxStart(5, one) != 0 {
		t.Error("an empty ring has nothing to show")
	}
	// A line taller than the pane is shown alone.
	if ls := r2.From(2, 2, rowsOf); len(ls) != 1 || first(ls) != 2 {
		t.Errorf("%v", ls)
	}
}
