package main

import (
	"fmt"
	"math/rand"
	"time"
)

// entry is one log line and the time it is written.
type entry struct {
	at   time.Time
	text string
}

// generator makes the lines of a unicorn server log, in the format of Ruby's
// Logger: "I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=0 ready".
//
// It keeps its own clock. next returns the lines of one event, and the caller
// decides whether to wait until their times.
type generator struct {
	rng     *rand.Rand
	clock   time.Time
	master  int
	nextPID int
	workers []int // the pid of each worker, by worker number
	config  string
	events  []weighted
	total   int
}

type weighted struct {
	weight int
	build  func(g *generator) []entry
}

func newGenerator(seed int64, start time.Time, workers int) *generator {
	g := &generator{rng: rand.New(rand.NewSource(seed)), clock: start, config: "/srv/app/config/unicorn.rb"}
	g.master = 2000 + g.rng.Intn(28000)
	g.nextPID = g.master
	g.workers = make([]int, workers)
	// Mostly requests that go well. Failures, and workers that restart, are
	// the exception.
	g.events = []weighted{
		{100, (*generator).request},
		{12, (*generator).debugNote},
		{2, (*generator).slowRequest},
		{4, (*generator).deprecation},
		{1, (*generator).appError},
		{2, (*generator).timeout},
		{2, (*generator).recycle},
		{1, (*generator).reload},
	}
	for _, e := range g.events {
		g.total += e.weight
	}
	return g
}

// tick moves the clock forward by up to max and returns the new time.
func (g *generator) tick(max time.Duration) time.Time {
	g.clock = g.clock.Add(time.Duration(g.rng.Int63n(int64(max)) + int64(time.Millisecond)))
	return g.clock
}

// line is a line of the log. Every line takes a few microseconds, so the times
// of a log are not all on the millisecond.
func (g *generator) line(level byte, pid int, format string, args ...any) entry {
	g.clock = g.clock.Add(time.Duration(5+g.rng.Intn(150)) * time.Microsecond)
	names := map[byte]string{'D': "DEBUG", 'I': "INFO", 'W': "WARN", 'E': "ERROR", 'F': "FATAL"}
	text := fmt.Sprintf("%c, [%s #%d] %5s -- : %s", level, g.clock.Format("2006-01-02T15:04:05.000000"), pid, names[level], fmt.Sprintf(format, args...))
	return entry{at: g.clock, text: text}
}

func (g *generator) pid() int {
	g.nextPID += 1 + g.rng.Intn(60)
	return g.nextPID
}

func (g *generator) pick(options ...string) string { return options[g.rng.Intn(len(options))] }

func (g *generator) worker() int { return g.rng.Intn(len(g.workers)) }

// start returns what a unicorn master prints when it boots.
func (g *generator) start() []entry {
	var out []entry
	add := func(max time.Duration, e func() entry) {
		g.tick(max)
		out = append(out, e())
	}
	add(5*time.Millisecond, func() entry { return g.line('I', g.master, "Refreshing Gem list") })
	add(30*time.Millisecond, func() entry { return g.line('I', g.master, "listening on addr=0.0.0.0:3000 fd=9") })
	add(10*time.Millisecond, func() entry { return g.line('I', g.master, "master process ready") })
	for n := range g.workers {
		g.workers[n] = g.pid()
		add(20*time.Millisecond, func() entry { return g.line('I', g.master, "worker=%d spawned pid=%d", n, g.workers[n]) })
	}
	for n := range g.workers {
		add(400*time.Millisecond, func() entry { return g.line('I', g.workers[n], "worker=%d ready", n) })
	}
	return out
}

// next returns the lines of the next event, after a pause of about mean.
func (g *generator) next(mean time.Duration) []entry {
	pause := time.Duration(g.rng.ExpFloat64() * float64(mean))
	g.clock = g.clock.Add(min(pause, 10*mean))
	roll := g.rng.Intn(g.total)
	for _, e := range g.events {
		if roll < e.weight {
			return e.build(g)
		}
		roll -= e.weight
	}
	return nil
}

// respawn replaces worker n after the master reaped it.
func (g *generator) respawn(n int, status string) []entry {
	old := g.workers[n]
	g.workers[n] = g.pid()
	out := []entry{g.line('I', g.master, "reaped #<Process::Status: pid %d %s> worker=%d", old, status, n)}
	g.tick(15 * time.Millisecond)
	out = append(out, g.line('I', g.master, "worker=%d spawned pid=%d", n, g.workers[n]))
	g.tick(900 * time.Millisecond)
	return append(out, g.line('I', g.workers[n], "worker=%d ready", n))
}

// recycle is a worker that exits on its own, such as after the memory limit.
func (g *generator) recycle() []entry {
	return g.respawn(g.worker(), "exit 0")
}

// timeout is a worker that the master kills because a request took too long.
func (g *generator) timeout() []entry {
	n := g.worker()
	limit := []int{30, 60, 60, 120}[g.rng.Intn(4)]
	took := limit + 1 + g.rng.Intn(4)
	out := []entry{g.line('W', g.master, "worker=%d PID:%d timeout (%ds > %ds), killing", n, g.workers[n], took, limit)}
	g.tick(30 * time.Millisecond)
	return append(out, g.respawn(n, "SIGKILL (signal 9)")...)
}

// deprecation is output that the application writes to stderr by itself, so
// it has no logger prefix.
func (g *generator) deprecation() []entry {
	text := g.pick(
		"DEPRECATION WARNING: Passing a block to ActiveRecord::Base.transaction is deprecated (called from index at /srv/app/app/controllers/orders_controller.rb:18)",
		"warning: already initialized constant Billing::MAX_RETRIES",
		"/srv/app/lib/legacy/exporter.rb:44: warning: method redefined; discarding old export",
	)
	return []entry{{at: g.clock, text: text}}
}

// debugNote is a quiet housekeeping line.
func (g *generator) debugNote() []entry {
	n := g.worker()
	return []entry{g.line('D', g.workers[n], "%s", g.pick(
		fmt.Sprintf("worker=%d reopening logs", n),
		fmt.Sprintf("worker=%d heartbeat tmp=/tmp/unicorn-%d.sock", n, g.workers[n]),
		fmt.Sprintf("worker=%d gc ran, heap_live_slots=%d", n, 300000+g.rng.Intn(400000)),
	))}
}

// reload is the master getting SIGHUP, as a deploy does.
func (g *generator) reload() []entry {
	out := []entry{g.line('I', g.master, "received SIGHUP, reloading config_file=%s", g.config)}
	g.tick(40 * time.Millisecond)
	out = append(out, g.line('I', g.master, "Refreshing Gem list"))
	g.tick(300 * time.Millisecond)
	return append(out, g.line('I', g.master, "done reloading config_file=%s", g.config))
}
