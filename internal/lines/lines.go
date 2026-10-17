// Package lines reads input in batches of lines on its own goroutine, so the
// consumer can also wait on timers and keys.
package lines

import (
	"bufio"
	"bytes"
	"io"
	"sync"
)

const (
	readerSize = 256 << 10
	// MaxChunk bounds memory for pathological input: a line longer than this
	// is delivered in pieces, all but the last without a newline, so that
	// writing them out one after another reproduces the input exactly.
	MaxChunk   = 1 << 20
	maxLines   = 4096
	maxBatchSz = 1 << 20
)

type span struct {
	start, end int
	nl         bool
}

// Batch is a run of lines. A batch ends when the reader has nothing more
// buffered, so a consumer can flush its output after each one.
type Batch struct {
	buf   []byte
	spans []span
	// EOF is true on the last batch. Err is a read error other than EOF.
	EOF bool
	Err error
}

// Len is the number of lines.
func (b *Batch) Len() int { return len(b.spans) }

// At returns line i without its newline, and whether it had one. The text is
// valid until Release.
func (b *Batch) At(i int) ([]byte, bool) {
	s := b.spans[i]
	return b.buf[s.start:s.end], s.nl
}

var pool = sync.Pool{New: func() any { return &Batch{} }}

// Release returns the batch's memory for reuse. The batch must not be used
// afterwards.
func (b *Batch) Release() {
	b.buf, b.spans, b.EOF, b.Err = b.buf[:0], b.spans[:0], false, nil
	pool.Put(b)
}

func newBatch() *Batch { return pool.Get().(*Batch) }

// Read reads r on a new goroutine and delivers its lines in batches. The
// channel is closed after the batch with EOF set. Closing done stops the
// goroutine early.
func Read(r io.Reader, done <-chan struct{}) <-chan *Batch {
	out := make(chan *Batch, 4)
	go func() {
		defer close(out)
		br := bufio.NewReaderSize(r, readerSize)
		cur := newBatch()
		send := func(final bool, err error) bool {
			cur.EOF, cur.Err = final, err
			select {
			case out <- cur:
			case <-done:
				return false
			}
			cur = newBatch()
			return true
		}
		start := 0
		for {
			frag, err := br.ReadSlice('\n')
			cur.buf = append(cur.buf, frag...)
			switch {
			case err == nil:
				cur.spans = append(cur.spans, span{start, len(cur.buf) - 1, true})
				start = len(cur.buf)
				if br.Buffered() == 0 || len(cur.spans) >= maxLines || len(cur.buf) >= maxBatchSz {
					if !send(false, nil) {
						return
					}
					start = 0
				}
			case err == bufio.ErrBufferFull:
				if len(cur.buf)-start >= MaxChunk {
					cur.spans = append(cur.spans, span{start, len(cur.buf), false})
					start = len(cur.buf)
				}
			default: // EOF or a read error
				if len(cur.buf) > start {
					cur.spans = append(cur.spans, span{start, len(cur.buf), false})
				}
				if err == io.EOF {
					err = nil
				}
				send(true, err)
				return
			}
		}
	}()
	return out
}

// Split is a convenience for tests and small inputs: it returns the lines of
// data without their newlines.
func Split(data []byte) [][]byte {
	data = bytes.TrimSuffix(data, []byte("\n"))
	if len(data) == 0 {
		return nil
	}
	return bytes.Split(data, []byte("\n"))
}
