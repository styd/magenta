# Troubleshooting


- `magenta doctor` shows the config directory, the colorscheme and theme, every rule file (built-in or user override), the color depth, the fzf it found and what that version supports, and the measured start-up time.
- A rule or colorscheme file with a mistake never stops a log from being shown: `mgnt` prints a warning and falls back (to the default colorscheme, or to the next rule).
- `tail -f app.log | magenta` is a common slip. `magenta` does not show logs and says so. Use `mgnt`.
