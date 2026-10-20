# Paging and searching

What `mgnt` does when output goes to a terminal: the pager, the followed log, the `/` search with fzf, and the preview pane.

# Paging and searching

When standard output is a terminal, `mgnt` is a pager as well as a colorizer, so `glo | mgnt` pages like `glo` itself would with `less` (git only starts its pager when its output is a terminal, which is why piping into something loses it). What it does depends on the input:

- **Short output** (it fits on the screen) is just printed and `mgnt` exits, like `less -F`.
- **A lot of output that arrives at once** (a command with its output ready, such as `git log`) opens a **full-screen pager that scrolls both ways**. The bottom two rows are laid out and colored like fzf's own header and info line: the keys in plain muted text, then the position (`lines 1-15 of 3749  0%`) followed by a rule across the width. While you page, `mgnt` reads only a few screens ahead, so the command writing into the pipe is held back, exactly as with `less`. `G` reads to the end.
- **A log that trickles in** (`tail -f app.log | mgnt`) is not paged: lines are printed as they arrive, into your normal terminal scrollback, which you scroll with the terminal as usual. Ctrl-C quits, and so does `q`.

Keys in the pager:

| | |
|---|---|
| `j`, `Down`, `Enter`, `Ctrl-N` | one line down |
| `k`, `Up`, `y`, `Ctrl-P` | one line up |
| `Space`, `f`, `PageDown`, `Ctrl-F` | one screen down |
| `b`, `PageUp`, `Ctrl-B` | one screen up |
| `d` / `u` | half a screen down / up |
| `g`, `Home` / `G`, `End` | the top / the end (`G` shows the end once the input has been quiet for a moment, so it works on a log that never ends) |
| `F` | **follow**: stay on the newest line as the input grows, like `less +F`. Any key stops it (a movement key also moves) |
| `w` | **chop** long lines instead of wrapping them (and back) |
| `h` / `l`, `Left` / `Right` | scroll sideways by half a screen, while chopping |
| `#` | show **line numbers** in a gutter |
| `n` / `N` | the **next / previous match** of your last search, wrapping at the ends |
| `/` | search with fzf (see below) |
| `q`, `Ctrl-C` | quit |

The mouse wheel scrolls too, in terminals that send arrow keys for the wheel on the alternate screen (`mgnt` turns that mode on. iTerm2, Terminal.app, kitty, WezTerm and xterm-based terminals do it, and tmux needs `mouse on`).

Things to know about the pager:

- It uses the terminal's **alternate screen**, like `less` does: when you quit, your terminal is exactly as it was before, and nothing from the log is left in its scrollback. (The followed-log view does not, and keeps native scrollback and selection.) Text can still be selected and copied with the mouse.
- You can scroll back through whatever the buffer holds: the last `buffer` lines (100,000 by default, and never fewer than 65,536). For a longer input the oldest lines are gone by the time you press `G`.
- Wrapped lines are counted by the rows they take, and scrolling moves by whole lines. The line numbers are those of the input (and of fzf's list: the hidden number in front of each of its lines).
- `n` and `N` use fzf's own matching: after a search, `mgnt` asks `fzf --filter` which lines match your query, with your `fzf_opts` (`--exact` and the like), so they agree with what the search found. They work on the lines read at that time, and are offered (the `n/N` hint) once the matches are in.
- Without fzf installed the pager works, and `/` just says there is no search. `TERM=dumb` terminals cannot be repainted, so they get plain printing.

Press **`/`** at any time to open **fzf** on the lines seen so far (up to `buffer` of them, 100,000 by default). Lines that arrive while fzf is open keep being added to its list. It is the real fzf: your `FZF_DEFAULT_OPTS`, key bindings and query syntax all apply. The prompt is at the bottom (mgnt passes `--layout default`, whatever `FZF_DEFAULT_OPTS` says, and you can override it with `fzf_opts` if you prefer another layout) and the newest line is nearest it.

- **Where the list starts:** a log that was already there (you were paged through it, like `git log`) is read from its first line, as in a pager: the list runs top to bottom starting at line 1, with the prompt at the bottom. A log that is still trickling in starts at its end, with the newest line nearest the prompt.
- **ctrl-o** accepts. In the pager it **jumps to the line** (the first one, if you marked several with Tab). In a followed log it prints the selected line(s), oldest first, into the scrollback and `mgnt` goes back to streaming, showing the lines that arrived meanwhile. **Enter** opens the preview instead (see below), and **Esc** leaves without doing anything. Change the key with `magenta config set keys.accept alt-p`, or set it to `enter` to give Enter back its usual job of printing.
- **Long lines** are shown whole: each matching line is wrapped over as many rows as it needs (a `↳` marks a continuation row), and not cut with `..`. This needs fzf 0.54.0 or newer. An older fzf can only stop the sideways scrolling and cuts a line at the edge of the screen. To get fzf's own cutting back, use `magenta config set fzf_opts --no-wrap`.
- The terminal is restored on every way out.

`mgnt` is a plain filter, and does not touch the terminal at all (no `/dev/tty`, no key handling, no paging), when output is not a terminal (`mgnt | less -R`), with `--no-interactive`, and on a system whose terminal it has no code for.

A search cannot stay on as a filter for the incoming stream: the fuzzy matching lives inside fzf.

## The preview pane

fzf's list only shows the lines that match, so the **preview pane** shows what surrounds the focused line: as many lines around it as fit, in their syntax colors, with the focused line marked by a `▶` in the margin. There are no line numbers, since in a log that streams they say nothing to the reader.

By default it sits **above** the list at half the height (`up,50%,wrap,border-bottom,hidden`) and starts **hidden**: typing and moving the cursor do not show it. **Enter** on a line opens it and moves you *into* it (its border says "Up/Down: scroll   Esc: back to the list"):

- **Up / Down** (also Ctrl-P/N/K/J) scroll the preview by a line, and **Page Up / Page Down** by a pane, while the list cursor stays where it is. The preview is not limited to the lines around the match: you can scroll through the whole buffer, up to its first line and down to its newest.
- **Esc** closes the preview and gives the keys back to the list. With the preview closed, Esc leaves fzf as usual.
- Opening it again starts centered on the focused line.
- **While it is open nothing else moves**, so you can study one result and its surroundings: the list is frozen (its label says `list paused`) and the preview keeps showing the line you chose. Lines that keep arriving from the log wait in `mgnt`'s buffer, nothing is lost, and they appear in the list when you press Esc.

This needs fzf 0.49.0 or newer. On older fzf the preview is simply always shown and Enter keeps printing the line. If you set your own `preview_window` without `hidden`, the preview is always shown and Enter and the movement keys are left alone. The keys run a small helper (`mgnt __preview --key ...`, one short process per key press) that tells fzf what to do, because fzf cannot decide by itself between scrolling the preview and moving the list.

- The **mouse wheel** over the preview scrolls it, three lines a notch.
- Toggle it with `ctrl-/` (like Enter and Esc, it opens and closes it. Change it with `magenta config set keys.preview_toggle alt-p`). Terminals do not pass the Cmd key to programs, so use a Ctrl or Alt combination. **On macOS, Alt combinations need your terminal to treat Option as Meta** (Terminal.app: Profiles > Keyboard > "Use Option as Meta key". iTerm2: Profiles > Keys > "Esc+").
- Turn it off with `magenta config set preview false`, and change the layout with `magenta config set preview_window "right,40%,wrap"`.
- How it works: while fzf is open, `mgnt` serves the lines over a Unix socket in a private temporary directory (mode 0700, removed afterwards). fzf runs `mgnt __preview` (a hidden subcommand) for each focused line. If that cannot reach `mgnt` it prints one dim line, `[mgnt] preview unavailable`, and nothing else.

## Older fzf versions

`fzf --version` runs once, the first time you press `/`. Anything older than the preview needs is handled without refusing to search, with one dim notice the first time:

| fzf | Behavior |
|-----|-----------|
| 0.49.0 or newer | full behavior: Enter opens the preview, Esc closes it |
| 0.27.0 up to 0.49.0 | preview pane with its border, but always shown (Enter prints, as usual) |
| 0.19.0 up to 0.27.0 | preview pane always shown, without the border |
| 0.18.0 up to 0.19.0 | the same, but no `ctrl-/` toggle (the key name does not exist yet) |
| older than 0.18.0 | no preview pane (the helper cannot learn the pane size) |

These versions were checked against the fzf git history (the first release containing each feature):

- 0.13.0: `--preview`, `--preview-window`, and `toggle-preview`
- 0.16.0: `wrap` in the preview window
- 0.17.4: `--layout`
- 0.18.0: `FZF_PREVIEW_LINES` and `FZF_PREVIEW_COLUMNS`
- 0.19.0: `ctrl-/` as a `--bind` key
- 0.27.0: `border-bottom` and the other `border-*` styles
- 0.37.0: `change-preview-label`
- 0.38.0: `show-preview` and `hide-preview`
- 0.43.0: the mouse wheel events over the preview
- 0.45.0: `transform`
- 0.49.0: `$FZF_PREVIEW_LABEL`, which is how the keys tell whether the preview is open
- 0.53.0: `--tail`, which lets fzf keep only as many lines as the buffer holds, so a search left open on a live log does not grow without bound
- 0.35.0: the `separator` color
- 0.36.0: the `scrollbar` color
- 0.54.0: `--wrap` for the list
- 0.58.0: the `input-bg` and `input-fg` colors, which give the search bar its own background

If you set `preview_window` or `fzf_opts` yourself, your flags are used exactly as written.
