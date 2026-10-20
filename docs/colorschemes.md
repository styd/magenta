# Colorschemes and themes

A **colorscheme** is a named set of highlighting colors, such as `magenta` or `mono`. The **theme** is its dark or light variant. They are two separate settings:

| Setting | Values | Default | One run |
|---|---|---|---|
| `colorscheme` | a name from `magenta colorscheme list` | `magenta` | `mgnt --colorscheme mono` |
| `theme` | `auto`, `dark`, or `light` | `auto` | `mgnt --theme light` |

```
magenta colorscheme list
magenta colorscheme use mono       # choose the colorscheme
magenta config set theme light     # choose the theme
```

`magenta` is the name of the project, of the command, and of the default colorscheme. These docs say "the magenta command" and "the magenta colorscheme" where it could be unclear.

With `theme = "auto"`, `mgnt` finds out whether the terminal background is dark or light, see below.

## The magenta colorscheme

The default. Purple leads, then light yellow, then light blue, with a soft rose for errors. Despite the name, there is no magenta in the log colors. The one magenta is for fzf's marker and hints. It has a dark and a light theme, and every color of a theme is named in the palette of the file:

| Role | Dark | Light | Used for |
|---|---|---|---|
| `background`, `mantle` | `#1f1933`, `#19142a` | `#f7f3ff`, `#efe9fb` | the terminal's base and alternate backgrounds, which the contrast tests use |
| `surface0` | `#2d2547` | `#ded4f2` | the `/` search bar |
| `surface2` | `#4e437a` | `#bfaedd` | a stronger surface, such as a selection |
| `text` | `#e4dcf7` | `#2a2040` | plain text, such as context lines of a diff |
| `comment` | `#9a8cc6` | `#6a5a99` | timestamps, dates, `DEBUG`, meta lines |
| `keyword` | `#c3a6ff` | `#6b3fd4` | HTTP verbs, refs such as `HEAD -> main`, hunk headers, constants |
| `operator` | `#a98bf0` | `#7a4be0` | punctuation (not used by a built-in rule yet) |
| `symbol` | `#e0b8ff` | `#a23fc4` | the key of `key=value` |
| `function` | `#f7e08a` | `#8a6500` | `WARN`, commit hashes, diff file headers, commands |
| `string` | `#fbf3c8` | `#6b6000` | paths, URLs, quoted strings |
| `number` | `#f2d49b` | `#a35f00` | numbers, durations, sizes, process ids |
| `type` | `#b4daff` | `#174f9c` | `INFO`, class and module names, authors, added lines |
| `param` | `#9fc8f7` | `#2f6fc0` | the value of `key=value`, IP addresses |
| `error` | `#f28fb0` | `#c0305f` | `ERROR` and `FATAL` (bold), removed lines |
| `magenta` | `#e66bc0` | `#ae1e81` | the only magenta, for fzf's marker and the keys on top of the search. It has the hue of the logo, and is lighter or darker to stay readable |

Colors are 24 bit. On a terminal with 256 or 16 colors, `mgnt` picks the nearest color it has. `NO_COLOR` turns them all off.

While the pager or the `/` search is open, `mgnt` also gives the terminal the `background` and `text` colors of the theme, so the pager looks the same on any terminal. It does that with the OSC 10 and 11 escape sequences, and puts your own colors back when it ends, even after Ctrl-C. A log that is only streamed or piped is never painted.

fzf draws the `/` search, so `mgnt` gives it the colors of the theme too, with `--color` and fzf's own `dark` or `light` base. The styles that start with `ui.` say which: `ui.search` is the search bar (`surface0`, from fzf 0.58.0), `ui.current` the line under the cursor (`surface2`), `ui.marker` the marker and pointer (the magenta), `ui.hint` the keys on top (the magenta), `ui.info` the count, `ui.border` the lines, and `ui.background` the strip left of the list. A color that your fzf is too old to know is left out, and the rest still work. The footer of the pager uses the same colors, so it looks the same as the search. Your `fzf_opts` and `MGNT_FZF_OPTS` come last and win. Colors in `FZF_DEFAULT_OPTS` lose to the colorscheme for the names it sets, because command line options beat that variable.

A terminal that ignores OSC 10 and 11 just keeps its colors. To keep your terminal's and fzf's colors in every case, use a copy of the colorscheme without the `ui.` styles. `mono` has none.

**To copy and tweak it**, start a file of your own from it, or override the built-in one:

```
magenta colorscheme new mine        # a copy of the magenta colorscheme named "mine"
magenta colorscheme edit mine       # opens $EDITOR, then checks both themes
magenta colorscheme use mine
magenta colorscheme edit magenta    # or: your copy replaces the built-in one
```

To change one color, change its hex value in `[palette.dark]` or `[palette.light]`. Every style that uses it follows. To restyle one kind of thing, point its style at another role, for example `"timestamp" = { fg = "operator" }`.

## A colorscheme file

```toml
name = "mycolors"

[palette.dark]                   # named colors for the dark theme
purple  = "#c3a6ff"
rose    = "#f28fb0"

[palette.light]                  # and for the light theme
purple  = "#6b3fd4"
rose    = "#c0305f"

[styles]
"level.error" = { fg = "rose", bold = true }
"level.info"  = { fg = "purple" }
"diff.added"  = { fg = "green" }          # ANSI names, 0-255 indexes and #hex all work
"timestamp"   = { fg = 244 }
"ui.marker"   = { fg = "rose" }                      # fzf marker and pointer
```

The styles are shared by both themes, so a name like `rose` has to be in both palettes. A colorscheme with the palette of one theme only uses it for both, and one with no palette uses only hex colors, indexes, and ANSI names. `magenta colorscheme check` tests both themes.

Attributes: `bold`, `dim`, `italic`, `underline`, and `bg`. Colors are downgraded automatically for 256-color and 16-color terminals.

The built-in colorschemes are `magenta` and `mono`. `mono` has no color at all, only bold, dim, and underline, so its theme makes no difference.

## Dark or light: `auto`

With `theme = "auto"` (the default), `mgnt` finds out whether the terminal background is dark or light and uses the dark or the light theme of the colorscheme. It looks, in this order, at:

1. The `MGNT_BACKGROUND` variable, `dark` or `light`. Use it to force a choice.
2. The terminal's own answer to the background color question (the OSC 11 escape sequence). Many modern terminals answer, but not all do. This is only asked when `mgnt` is a pager on a terminal.
3. The `COLORFGBG` variable, which some terminals set.
4. Otherwise it assumes dark.

When `mgnt` is a plain filter (output piped), it never talks to the terminal, so it uses only 1, 3 and 4. A terminal that ignores the question costs a wait of up to 150 ms when `mgnt` starts as a pager. If that bothers you, pin a theme with `magenta config set theme dark`, or set `MGNT_BACKGROUND`. `magenta doctor` shows what `auto` decides from the environment. A pinned `dark` or `light` never asks the terminal.
