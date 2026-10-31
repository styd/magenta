#!/bin/sh
# End to end check of mgnt as a pager with the real fzf, driven through tmux.
# Needs go, tmux and fzf. Run it from the repository root: sh test/smoke.sh
# It uses a private tmux server, so a tmux of your own is never touched.
set -eu

for tool in go tmux fzf; do
  command -v "$tool" >/dev/null 2>&1 || { echo "smoke: $tool is required" >&2; exit 2; }
done

work=$(mktemp -d)
sock=mgsmoke$$
trap 'tmux -L "$sock" kill-server 2>/dev/null || true; rm -rf "$work"' EXIT

go build -o "$work/" ./cmd/mgnt
{
  i=1
  while [ "$i" -le 200 ]; do
    echo "2026-01-01 10:00:00 INFO request $i served in 12ms filler filler filler"
    i=$((i + 1))
  done
  echo "2026-01-01 10:01:00 ERROR the kraken line failed"
} > "$work/log"

# A terminal environment that does not depend on the machine running this.
export XDG_CONFIG_HOME="$work/config" MGNT_BACKGROUND=dark
tmux -L "$sock" -f /dev/null new-session -d -s main -x 100 -y 30 \
  "env XDG_CONFIG_HOME=$XDG_CONFIG_HOME MGNT_BACKGROUND=dark TERM=xterm-256color $work/mgnt < $work/log; echo mgnt-exit-\$?; sleep 30"

target=main
screen() { tmux -L "$sock" capture-pane -p -t "$target"; }
send() { tmux -L "$sock" send-keys -t "$target" "$@"; }

# wait_for TEXT: poll the screen for up to 10 seconds.
wait_for() {
  n=0
  while [ "$n" -lt 100 ]; do
    screen | grep -qF -- "$1" && return 0
    sleep 0.1
    n=$((n + 1))
  done
  echo "smoke: never saw '$1'. The screen is:" >&2
  screen >&2
  exit 1
}

# wait_gone TEXT: the opposite.
wait_gone() {
  n=0
  while [ "$n" -lt 100 ]; do
    screen | grep -qF -- "$1" || return 0
    sleep 0.1
    n=$((n + 1))
  done
  echo "smoke: '$1' never went away. The screen is:" >&2
  screen >&2
  exit 1
}

# A log that does not fit one screen is paged from its top.
wait_for "request 1 served"
send /
wait_for "201/201"                  # fzf shows how many lines it holds
send "kraken"
wait_for "kraken line failed"
wait_gone "request 150 served"         # lines that do not match are gone
# Enter opens the preview, which marks the focused line and has no line numbers.
send Enter
wait_for "▶ "
# Esc closes the preview, and Esc again leaves the search.
send Escape
wait_gone "▶ "
send Escape
wait_for "request 1 served"
# q leaves the pager and mgnt exits cleanly.
send q
wait_for "mgnt-exit-0"

# Piped man output marks bold with backspaces (N, backspace, N). mgnt turns
# them into plain text, so the man rule can match the headings and color them.
# (fzf resolves backspaces by itself, so the search would work either way.)
bs=$(printf '\b')
bold() { printf '%s' "$1" | sed "s/./&$bs&/g"; }
{
  printf 'FAKE(1)    General Commands Manual    FAKE(1)\n\n'
  printf '%s\n' "$(bold NAME)"
  printf '     fake - a page for the smoke check\n\n'
  printf '%s\n' "$(bold DESCRIPTION)"
  i=1
  while [ "$i" -le 60 ]; do
    echo "     Line $i of the description."
    i=$((i + 1))
  done
  printf '     The %s is hiding here.\n' "$(bold kraken)"
} > "$work/page"
tmux -L "$sock" -f /dev/null new-session -d -s man -x 100 -y 30 \
  "env XDG_CONFIG_HOME=$XDG_CONFIG_HOME MGNT_BACKGROUND=dark TERM=xterm-256color $work/mgnt < $work/page; echo mgnt-exit-\$?; sleep 30"
target=man
wait_for "General Commands Manual"
# The heading is colored, which only happens when the rule saw it as plain NAME.
tmux -L "$sock" capture-pane -e -p -t "$target" | grep -q "$(printf '\033')\[[0-9;]*mNAME" || {
  echo "smoke: the NAME heading of the man page is not colored. The screen is:" >&2
  screen >&2
  exit 1
}
send /
wait_for "67/67"                      # fzf holds every line of the page
send "'kraken"
wait_for "1/67"                       # one match: the bold word
wait_for "is hiding here"
send Escape
send Escape
send q
wait_for "mgnt-exit-0"
echo "smoke: ok"
