# Development

## Requirements

- Go, at the version in `go.mod` (the dependencies need Go 1.26 or newer).
- `fzf` for the search. Tests that need the real one skip themselves when it is not installed, and everything else uses a fake.
- `tmux` is useful for trying the terminal behavior by hand (see below). Only `make smoke` needs it, together with the real `fzf`.

## Build and test

```
make build      # bin/mgnt and bin/magenta, with the version stamped in
make test       # go vet and go test ./...
make bench      # the engine and start-up benchmarks
make install    # go install of both commands
make smoke      # end to end check of the pager and search with the real fzf, through tmux
```

Before sending a change, run `gofmt -l .` (it should print nothing), `go vet ./...`, and `go test ./...`. For anything that touches goroutines, channels, or the terminal code, also run `go test -race ./...`. The whole suite takes under a minute. CI runs it on Linux, macOS, and Windows, then the smoke check on Linux and macOS, then Alpine (musl), then FreeBSD, OpenBSD, and NetBSD in virtual machines.

Some tests are timing sensitive on a slow machine. `MGNT_STARTUP_BUDGET_MS=50 go test ./internal/mgnt` raises the start-up budget (10 ms by default) for the one test that measures it.

## Layout

```
cmd/mgnt        the log viewer (a thin main)
cmd/magenta     the rule and colorscheme manager (a thin main)
assets.go       embeds rules/ and colorschemes/ into both binaries
rules/          built-in rule files (TOML)
colorschemes/   built-in colorschemes (TOML)
testdata/       one folder per built-in rule: in.log and the expected out.dark.ansi and out.light.ansi
internal/
  cli           the hints for using the wrong command, the version string
  config        config.toml, the config directory, finding built-in and user files
  rules         parsing, validating, and compiling rule files, the Loader interface
  colorscheme   colorscheme files and scope resolution (longest prefix wins)
  render        color depth, ANSI output, cutting and wrapping colored lines
  engine        applies a compiled rule file to one line at a time
  detect        scores the rule files against the first lines of a log
  lines         reads input in batches on its own goroutine
  pipe          samples the start of the log, picks a rule, colors the lines
  buffer        the ring buffer with sequence numbers
  tty           cbreak mode and reading keys from /dev/tty (Unix only)
  search        fzf: version handling, the feed, the preview server, and the helper
  mgnt          the mgnt command: plain filter, follow view, pager
  magentacli    the magenta command (cobra)
```

## How a line travels

1. `lines.Read` reads the input on a goroutine and sends batches. A batch ends when the reader has nothing buffered, which is also when output is flushed.
2. `pipe.Pipeline` holds the first lines (up to 10, or 50 ms) while `detect` chooses a rule file, then locks on. It re-checks only after 100 lines in a row that the rule does not recognize.
3. `engine.Highlighter` colors one line at a time. Rules claim bytes in order, so an earlier rule wins where matches overlap. Contexts (`enter` and `exit` tables) form a small state machine between lines. Removing the escape sequences from the output must always give the input back, byte for byte. A fuzz test guards this.
4. In `internal/mgnt`, a plain filter writes the lines out. On a terminal the lines go into the `buffer` ring first and a `view` shows them. It starts undecided, then becomes `follow` (printed as they arrive, into the scrollback) or `paging` (the full-screen pager) depending on whether a lot of output arrives at once.
5. `/` stops reading keys, gives the terminal back, and runs fzf through `search.Searcher`. It feeds fzf the whole ring and then each new line. While the fzf preview is open the feed is paused. The preview is drawn by `mgnt __preview`, which fzf runs for each focused line. It asks the running mgnt for the pane over a Unix socket. The same helper decides what the arrow keys and Esc do while the preview is open.

The ring buffer gives every line a number that is never reused. fzf is given `number<TAB>line` and shows only the text, so a selection comes back as a number and can be looked up for its colors.

## Testing

| Kind | Where | Notes |
|---|---|---|
| Golden files | `internal/engine` | `go test ./internal/engine -update` rewrites `testdata/*/out.dark.ansi` and `out.light.ansi`, the output of the magenta colorscheme in both themes. Read the diff before accepting it. |
| Fuzzing | `internal/engine` | `go test ./internal/engine -run xxx -fuzz FuzzHighlight -fuzztime 30s` |
| Property tests | `internal/render` | wrapping and cutting keep the text and the widths |
| A virtual terminal | `internal/mgnt/vscreen_test.go` | replays what a program wrote and says what is on the screen. The pager tests assert on that instead of on escape sequences. |
| A fake terminal | `internal/mgnt/interactive_test.go` | `fakeTerm` records cbreak and restore, and feeds keys |
| A fake fzf | `interactive_test.go` and `internal/search` | a shell script on a temporary path that records its arguments and what it was fed |
| Real pseudo terminals | `internal/tty` | cbreak, restore, the key reader, window size |
| The real fzf | `internal/search` | ordering, query matching. Skipped when fzf is missing. |
| The whole program in tmux | `test/smoke.sh` | builds `mgnt`, pages a log, searches, opens and closes the preview, and quits. It needs `tmux` and `fzf`, and uses a private tmux server. |

Things that make tests dependable: the Env structs of `mgnt` and `magenta` replace the terminal, signals, `PATH` lookups, standard streams, and the environment. Tests set `XDG_CONFIG_HOME` to a temporary directory so a developer's own config never leaks in. Tests that wait for output poll with a deadline instead of sleeping.

The version thresholds for fzf features live in `internal/search/fzfversion.go`, each with the commit that introduced the feature. Check new ones against fzf's git history (`git log -S` on the option name, then the first tag that contains the commit), not against summaries of its changelog.

## Trying the terminal behavior by hand

The pager and the search are best checked in a real terminal. A private tmux server keeps this safe and repeatable. Never run `tmux kill-server` without `-L`, because that would end your own sessions.

```
go build -o /tmp/mg/mgnt ./cmd/mgnt
T() { tmux -L mgdev -f /dev/null "$@"; }
T new-session -d -s t -x 100 -y 30 "env XDG_CONFIG_HOME=/tmp/mg/cfg TERM=xterm-256color bash --norc --noprofile"
T send-keys -t t 'git log --oneline | /tmp/mg/mgnt' Enter
T send-keys -t t j j j        # keys
T capture-pane -t t -p        # what is on the screen (add -e to see colors)
T kill-server                 # only the private one
```

## Adding a rule file

1. Write `rules/<name>.toml` with `name`, `detect` patterns, optional `filenames` globs, `sample` lines, and the `[[context.default.rule]]` tables. `magenta rule check rules/<name>.toml` reports problems with file and line.
2. Add `testdata/<name>/in.log` with a realistic log (include the awkward lines) and run the golden update once.
3. Add the name to the list in `TestExpectedBuiltins` (`internal/rules`). The other tests pick the file up by themselves: validity, samples being matched, byte preservation, and detection of its own log.
4. Use only scopes the colorschemes style. Add a new scope to the `magenta` colorscheme, and keep one hue for one kind of thing across rules.

## Adding a colorscheme

Add `colorschemes/<name>.toml`, and list it in the colorscheme tests (`internal/colorscheme`). Colors that are meant for reading have to meet the contrast numbers in `TestContrast` (4.5:1 against the background of the theme, and 4:1 for comments). Keep the colors in the palette and the styles pointing at them. The Go code has no color values of its own.

## Adding a setting
