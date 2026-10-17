package lines

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func collect(t *testing.T, r io.Reader) (text string, batches int, err error) {
	t.Helper()
	done := make(chan struct{})
	defer close(done)
	var out bytes.Buffer
	for b := range Read(r, done) {
		batches++
		for i := 0; i < b.Len(); i++ {
			l, nl := b.At(i)
			out.Write(l)
			if nl {
				out.WriteByte('\n')
			}
		}
		err = b.Err
		b.Release()
	}
	return out.String(), batches, err
}

func TestReadReproducesInput(t *testing.T) {
	big := strings.Repeat("y", MaxChunk*2+7)
	for _, in := range []string{"", "a", "a\n", "a\nb", "\n\n\n", "a\r\nb\r\n", "x\n" + big + "\nz", big, strings.Repeat("line\n", 10000)} {
		got, _, err := collect(t, strings.NewReader(in))
		if err != nil || got != in {
			t.Errorf("input of %d bytes: err %v, got %d bytes", len(in), err, len(got))
		}
	}
}

func TestReadWithTinyReads(t *testing.T) {
	in := "one\ntwo\nthree"
	got, batches, err := collect(t, iotest.OneByteReader(strings.NewReader(in)))
	if err != nil || got != in {
		t.Errorf("%q %v", got, err)
	}
	if batches < 2 {
		t.Errorf("a slow reader should give several batches, got %d", batches)
	}
}

func TestReadReportsErrorAfterLines(t *testing.T) {
	r := io.MultiReader(strings.NewReader("a\nb\n"), iotest.ErrReader(io.ErrClosedPipe))
	got, _, err := collect(t, r)
	if got != "a\nb\n" || err != io.ErrClosedPipe {
		t.Errorf("%q %v", got, err)
	}
}

func TestReadStopsWhenDone(t *testing.T) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	ch := Read(pr, done)
	go func() {
		for i := 0; i < 100000; i++ {
			if _, err := io.WriteString(pw, strings.Repeat("x", 1000)+"\n"); err != nil {
				return
			}
		}
		pw.Close()
	}()
	<-ch
	close(done)
	pr.Close()
	for range ch { // drains and ends
	}
}

func TestSplit(t *testing.T) {
	if got := Split([]byte("a\nb\n")); len(got) != 2 || string(got[1]) != "b" {
		t.Errorf("%q", got)
	}
	if Split(nil) != nil {
		t.Error("empty input has no lines")
	}
}
