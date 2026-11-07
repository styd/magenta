// Command streamlog writes a made-up unicorn server log that grows over time,
// to try mgnt on a log that keeps coming:
//
//	go run ./test/streamlog unicorn.log &
//	tail -f unicorn.log | mgnt
//
// The file is appended to, like a real log. With "-" the lines go to standard
// output, so "go run ./test/streamlog - | mgnt" works too.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("streamlog", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: streamlog [flags] FILE")
		fmt.Fprintln(stderr, "\nStreams a made-up unicorn log to FILE (appending), or to standard output for \"-\".")
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
	}
	interval := fs.Duration("interval", 300*time.Millisecond, "average pause between events")
	workers := fs.Int("workers", 4, "number of unicorn workers")
	count := fs.Int("lines", 0, "stop after this many lines (0: run until interrupted)")
	seed := fs.Int64("seed", time.Now().UnixNano(), "random seed, for the same log again")
	truncate := fs.Bool("truncate", false, "empty FILE first instead of appending")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 || *workers < 1 || *interval <= 0 || *count < 0 {
		fs.Usage()
		return 2
	}

	out := stdout
	if name := fs.Arg(0); name != "-" {
		flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
		if *truncate {
			flags |= os.O_TRUNC
		}
		f, err := os.OpenFile(name, flags, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "streamlog: %v\n", err)
			return 1
		}
		defer f.Close()
		out = f
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	g := newGenerator(*seed, time.Now(), *workers)
	if err := stream(ctx, g, out, *interval, *count); err != nil {
		fmt.Fprintf(stderr, "streamlog: %v\n", err)
		return 1
	}
	return 0
}

// stream writes each line when its time comes, until count lines were written
// (when it is not 0) or ctx is done. Every line is one write, so a reader that
// follows the file never sees half of one.
func stream(ctx context.Context, g *generator, out io.Writer, interval time.Duration, count int) error {
	written := 0
	batch := g.start()
	for {
		for _, e := range batch {
			if count > 0 && written >= count {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Until(e.at)):
			}
			if _, err := io.WriteString(out, e.text+"\n"); err != nil {
				return err
			}
			written++
		}
		if count > 0 && written >= count {
			return nil
		}
		batch = g.next(interval)
	}
}
