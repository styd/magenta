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

screen() { tmux -L "$sock" capture-pane -p -t main; }
send() { tmux -L "$sock" send-keys -t main "$@"; }

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
echo "smoke: ok"
