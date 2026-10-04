# iterm2-claude-bridge

iTerm2's [Claude Code integration](https://iterm2.com/claude-code-integration.html) shows you
which of your Claude sessions is working, waiting for you, or finished — for sessions running on
your Mac. This does the same for sessions running on **another machine, inside tmux, over SSH**,
and opens a tab that SSHes in and attaches when you click one.

[![CI](https://github.com/taumatix/iterm2-claude-bridge/actions/workflows/ci.yml/badge.svg)](https://github.com/taumatix/iterm2-claude-bridge/actions/workflows/ci.yml)

> **No automated test here talks to a real iTerm2 or a real SSH server.** The logic is covered
> end to end — including the hook recording a status from inside a real tmux pane — but those two
> edges are driven by fakes, because the machine this was built on has iTerm2's API switched off
> and no key authorised for its own `sshd`. One user has run v0.1.0 against a real iTerm2, which
> is how the bug that stopped every click being served was found; what that run settled and what
> it did not is in [what is and is not verified](UPSTREAM.md#what-is-and-is-not-verified). Read it
> before you rely on this.

## How it works

One binary, two roles.

```
 remote host (Linux, macOS, …)                    your Mac
 ┌──────────────────────────────┐                ┌────────────────────────────┐
 │ claude ──hook──▶ events.jsonl│                │ watch ──▶ toolbelt panel   │
 │                      │       │                │   │                        │
 │                   stream ────┼──── ssh ──────▶│   └─▶ click ──▶ new tab:   │
 │                              │                │       ssh host -t tmux     │
 │ tmux session "work"          │◀───────────────┼────── attach-session       │
 └──────────────────────────────┘                └────────────────────────────┘
```

Claude Code hooks on the remote host append a status line to a file. The Mac runs
`ssh host iterm2-claude-bridge stream` per host and keeps a list. Clicking a row activates the
tab already attached to that tmux session, or opens one that attaches.

No inbound ports, no daemon to expose: the only channel is an SSH connection you already have.

## Install

Go 1.27 or newer, on both sides.

```sh
go install github.com/taumatix/iterm2-claude-bridge/cmd/iterm2-claude-bridge@v0.4.0
```

### On each remote host

```sh
iterm2-claude-bridge install-hooks          # prints what it would change
iterm2-claude-bridge install-hooks --write  # merges it into ~/.claude/settings.json
```

It prints by default because `~/.claude/settings.json` is yours and may already hold hooks this
program knows nothing about. `--write` backs the file up to `settings.json.bak` first, keeps
every setting it does not understand, and is safe to run twice.

Then use Claude there inside tmux, and check it recorded something:

```sh
iterm2-claude-bridge list
# waiting   abc123def…   work   /srv/app
```

If that is empty, the usual cause is `iterm2-claude-bridge` not being on the `PATH` Claude Code
sees. Point the hook at an absolute path instead:

```sh
iterm2-claude-bridge install-hooks --write --command /usr/local/bin/iterm2-claude-bridge
```

### On your Mac

Enable iTerm2's API: **Settings → General → Magic → Enable Python API**. Then:

```sh
iterm2-claude-bridge watch --host build-box --host gpu-box
```

The first run raises an iTerm2 permission prompt. The panel appears in the toolbelt — if you
cannot see it, open **View → Toolbelt**.

Host names are whatever your `~/.ssh/config` calls them, so that is where the login name, port,
jump host and identity belong. For what it cannot express:

```sh
iterm2-claude-bridge watch --host build-box --ssh-arg -J --ssh-arg bastion
```

## The panel

When iTerm2 quits or restarts (it does on every update), `watch` keeps running. It reconnects when
iTerm2 is back and registers the panel again; a click made in between is refused with a message
saying iTerm2 is not connected, rather than failing on a dead socket. A reconnect asks iTerm2 for a
fresh cookie over AppleScript, so macOS may ask once whether this program may control iTerm2.

One row per Claude session, ordered so the ones wanting you come first:

| | |
|---|---|
| **needs you** | Claude is waiting for input or a permission decision |
| **working** | running a prompt or a tool |
| **idle** | finished responding |
| **ended** | the session has gone; the row disappears shortly after |

A row also shows how long ago its status last changed. A **working** session reports on every
tool call, so one that has said nothing for ten minutes has most likely lost its hook (Claude was
killed, or the machine slept). Its row then reads `working? · no update for 12m`, with a hollow dot,
so a stale status does not pass for a current one. Change the threshold with
`watch --stale-after 30m`, or turn it off with `--stale-after 0`. A session whose Claude
exits without saying so (killed, crashed) is noticed on its host instead: the hook records which
Claude process it ran for, and `stream` checks every ten seconds (`stream --check-processes`) and
reports the session ended once that process is gone. That needs both halves at v0.4.0 or later.

Each row shows the host and working directory. Clicking one brings up its tab, opening
`ssh <host> -t tmux select-window -t <window> ';' attach-session -t <session>` if there is none.

A session not running under tmux is shown but not clickable — there is nothing to attach to.

The tab opens in the window you are looking at, next to the toolbelt you clicked, not in a new
window.

New tabs are tagged with an iTerm2 session variable (`user.iterm2ClaudeBridge`), so clicking the
same row twice brings the existing tab forward rather than opening another. That survives
restarting the bridge, because the tag is read back from iTerm2 rather than remembered. A tab
somebody *else* opened — one where you attached to the same tmux session by hand — is not
recognised, so clicking opens a second one — [ROADMAP.md](ROADMAP.md), *Only a tab this program
opened can be found again*.

`ssh` runs through your login shell, interactively, so `~/.zshrc` and friends are sourced before
it starts. Without that a `SSH_AUTH_SOCK` you export from a dotfile is missing and key
authentication fails in the new tab while working everywhere else.

On the far side there is no login shell: ssh runs the attach command in a non-interactive
session, whose `PATH` often has no `/opt/homebrew/bin`. So the remote half reports where its tmux
is — it runs inside the pane, where tmux is on the `PATH` by definition — and the Mac invokes
that path. A remote host running a build older than v0.1.3 sends no path, and you get
`command not found: tmux` until you upgrade it.

A tmux session whose name contains `\(` cannot be clicked: iTerm2 evaluates a profile's command
as an interpolated string before running it, so that sequence would run an iTerm2 expression
rather than reaching the shell. Renaming the session is the fix.

## Why a second panel

iTerm2's built-in Session Status tool is iTerm2's, and its documentation describes no way for
another program to contribute rows to it — nor anything about remote hosts. So this registers its
own web-view tool next to it rather than guessing at an internal that would break on the next
iTerm2 release. One combined list would be better, and needs something from iTerm2 upstream;
see [ROADMAP.md](ROADMAP.md).

## Security

- The panel listens on **loopback only** and requires a random token, which iTerm2 receives in
  the tool's URL. Loopback alone would not be enough: any web page you visit can POST to
  `127.0.0.1`.
- Working directories and tmux session names come from another machine, so the panel escapes
  them as HTML and the SSH command shell-quotes them. Both are tested — the quoting by running
  the built command through a real shell and checking `tmux` receives one argument.
- The event log is `0600` in a `0700` directory, because working directory paths say something
  about what you are working on.
- Nothing is written to `~/.claude` except the hooks you ask for with `--write`.

## Testing

```sh
go test ./...          # no iTerm2, no SSH server, no network needed
go test -race ./...
```

Tests drive real things wherever one is available: a real tmux session, the real built binary
over a real pipe, a real HTTP server, a real `/bin/sh`. iTerm2 and `ssh` are the two edges
covered only by fakes, and [UPSTREAM.md](UPSTREAM.md) says exactly what that leaves unproven.

## Licence

**[GPL-2.0-or-later](LICENSE)** — copyleft. It links [iterm2-go](https://github.com/taumatix/iterm2-go),
which carries a verbatim copy of iTerm2's `api.proto` and is GPL-2.0-or-later for that reason, so
this cannot be more permissive. Read the licence before depending on it.
