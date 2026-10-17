package mgnt

import (
	"fmt"
	"io"
	"os"

	"github.com/styd/magenta/internal/lines"
)

// item is one thing from the inputs: the start of a new input, a batch of its
// lines, or a failure to open it.
type item struct {
	start    bool   // a new input begins
	filename string // its file name, "" for stdin
	batch    *lines.Batch
	err      error
}

// readSources reads the files one after the other (stdin when there are none)
// on a new goroutine. The channel is closed after the last input. Closing done
// stops it early.
func readSources(env Env, files []string, done <-chan struct{}) <-chan item {
	out := make(chan item, 4)
	if len(files) == 0 {
		files = []string{"-"}
	}
	send := func(it item) bool {
		select {
		case out <- it:
			return true
		case <-done:
			return false
		}
	}
	go func() {
		defer close(out)
		for _, name := range files {
			var in io.Reader = env.Stdin
			display := ""
			if name != "-" {
				f, err := os.Open(name)
				if err != nil {
					if !send(item{err: fmt.Errorf("%v", err)}) {
						return
					}
					continue
				}
				defer f.Close()
				in, display = f, name
			}
			if !send(item{start: true, filename: display}) {
				return
			}
			for b := range lines.Read(in, done) {
				if !send(item{batch: b}) {
					return
				}
			}
		}
	}()
	return out
}
