# Settings

All settings live in `config.toml` and can be changed for one run with `mgnt` options.

`~/.config/magenta/config.toml` (or `$XDG_CONFIG_HOME/magenta/config.toml`). Change a setting with an editor or `magenta config set <key> <value>`. `mgnt` reads the file each time it starts.

| Key | Default | |
|-----|---------|---|
| `colorscheme` | `magenta` | the colorscheme to use, a name from `magenta colorscheme list` |
| `theme` | `auto` | `dark` or `light`, which palette of the colorscheme to use. `auto` picks one by the terminal background |
| `buffer` | `100000` | lines kept for the `/` search |
| `ansi` | `strip` | `strip` removes colors already in the input, and `keep` passes lines through untouched |
| `fzf_opts` | | extra fzf options, added after the ones `mgnt` needs |
| `preview` | `true` | show the preview pane |
| `preview_window` | `up,50%,wrap,border-bottom,hidden` | fzf's `--preview-window`. With `hidden` the preview opens on Enter and closes on Esc |
| `[keys] preview_toggle` | `ctrl-/` | key that opens or closes the preview |
| `[keys] accept` | `ctrl-o` | key that prints the selected lines (`enter` gives Enter that job back) |

The environment variable `MGNT_FZF_OPTS` adds fzf options after `fzf_opts`.

## Changing a setting for one run

Options of `mgnt` override `config.toml` for that run only and never write the file. The file is read first, then the options.

| Option | Same as | Example |
|---|---|---|
| `--colorscheme NAME` | `colorscheme` | `mgnt --colorscheme mono app.log` |
| `--theme auto\|dark\|light` | `theme` | `mgnt --theme light app.log` |
| `--ansi strip\|keep` | `ansi` | `mgnt --ansi keep app.log` |
| `--buffer LINES` | `buffer` | `tail -f app.log \| mgnt --buffer 500000` |
| `--fzf-opts "..."` | `fzf_opts` | `mgnt --fzf-opts "--exact --no-mouse"` |
| `--no-preview` | `preview = false` | `mgnt --no-preview` |
| `--set KEY=VALUE` | any setting above, including `preview_window` and `keys.*` | `mgnt --set preview_window=right,40% --set keys.accept=enter` |

`--set` can be repeated and is applied last, so it wins over the other options. A value that is not valid is refused with exit status 2 before any input is read, for example `--buffer lots`. These options belong to `mgnt`. `magenta` answers that they do.

## Old spellings

Before colorschemes had a setting of their own, `theme` named what is now a colorscheme. The old spellings still work, and `mgnt` and `magenta` say what to write instead:

| Old | Now |
|---|---|
| `theme = "mono"` in `config.toml` | `colorscheme = "mono"` |
| `theme = "magenta-dark"` or `"magenta-light"` | `colorscheme = "magenta"` and `theme = "dark"` or `"light"` |
| `mgnt --theme mono`, `--set theme=mono` | `mgnt --colorscheme mono`, `--set colorscheme=mono` |
| `magenta theme list`, `new`, `use`, and the rest | `magenta colorscheme list`, `new`, `use`, and the rest |
| `magenta theme use auto` | `magenta config set theme auto` |
| `magenta config set theme mono` | `magenta config set colorscheme mono` |

A directory `themes` in the config directory is not read any more. Move its files to `colorschemes`, and put their `[palette]` colors under `[palette.dark]` and `[palette.light]`. `magenta doctor` points out the old directory and the old settings.
