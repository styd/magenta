# Writing rules

Rules tell `mgnt` how to color one kind of input. Colorschemes decide what the colors are, see [Colorschemes and themes](colorschemes.md).

Built-in rules and colorschemes are inside the binaries. Files in the config directory add to them, and a file with the **same `name`** as a built-in one replaces it.

```
magenta init                  # copy the built-ins into the config dir for editing
magenta rule list
magenta rule new myapp        # a starter rule from a template
magenta rule edit unicorn     # opens $EDITOR on your copy, then checks it
magenta rule check            # errors with file and line
magenta preview myapp         # see the sample lines colored
magenta detect app.log        # how each rule scores a file, and which one mgnt picks
```

`edit` makes your copy from the built-in one if needed, opens `$VISUAL` / `$EDITOR`, and runs `check` when the editor exits.

## A rule file

```toml
name = "unicorn"
detect = ['^[IWEFD], \[\d{4}-\d\d-\d\dT[\d:.]+ #\d+\]']   # regexes for auto-detection
filenames = ["unicorn*.log"]                            # optional: a strong hint
sample = ['I, [2026-10-06T10:00:00.123 #4242]  INFO -- : worker=0 ready']

[[context.default.rule]]
regex    = '^([IWEFD]), \[([^\]]+) #(\d+)\]'
captures = ["level.letter", "timestamp", "pid"]   # one scope per capture group, "" skips one

[[context.default.rule]]
regex = '\bworker=\d+'
scope = "key"                                      # one scope for the whole match

# Optional multi-line state:
[[context.default.enter]]
regex = '^Traceback'
to    = "stacktrace"

[[context.stacktrace.rule]]
regex = '^\s+File "'
scope = "stack.frame"

[[context.stacktrace.exit]]
unless = '^\s+File "'     # the first line that does not match leaves the context
                           # and is processed again in "default"
```

- Regexes are Go's [RE2](https://github.com/google/re2/wiki/Syntax) syntax: no lookaround, no backreferences.
- **Rule order is priority.** Where matches overlap, the earlier rule keeps the bytes.
- A rule can use `prefix = "+"` instead of (or with) `regex` to test the start of a line cheaply, and `stop = true` to end rule processing for a line once it matches. The built-in `git-diff` uses both.
- Scopes are dotted names (`level.error`, `http.status.5xx`). A colorscheme that styles `string` also styles `string.quoted`.
- `magenta rule check` rejects invalid regexes (with file and line), unknown contexts, context cycles, a `captures` list that does not match the regex's groups, and any `sample` line that no rule matches. It warns about scopes that the built-in colorscheme does not style.
- **Detection**: `mgnt` looks at the first 10 lines (or whatever arrived within 50 ms), scores each rule by how many of those lines its `detect` regexes match, and picks the best one that reaches a threshold, otherwise `generic`. A matching `filenames` glob adds a strong bonus. It re-checks only after 100 lines in a row that the chosen rule does not recognize. `magenta detect <file>` shows the scores.

Built-in rules: `generic`, `git-log`, `git-diff`, `git-status`, `jj-log`, `jj-status`, `jj-diff`, `go-test`, `pytest`, `compiler`, `docker-build`, `terraform`, `curl-verbose`, `ping`, `syslog`, `unicorn`, `cron`, `rails`, `nginx-access`, `jsonl`.
