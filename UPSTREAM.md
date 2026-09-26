# Upstream

This program sits between two things it does not control — Claude Code's hooks and
iTerm2's API — and shells out to two more, `ssh` and `tmux`. All four can change
without us. This file records what each was checked against and when.

`upstream_test.go` parses the block below and fails if a pin has no date, so an
unrefreshed pin cannot hide.

```yaml
- name: claude-code-hooks
  kind: docs
  url: https://code.claude.com/docs/en/hooks
  checked: 2026-09-26
  note: >-
    the hook event names and the payload fields. Retrieved and read on 2026-09-26,
    not recalled. Nine events are acted on (SessionStart, UserPromptSubmit,
    PreToolUse, PostToolUse, Notification, PermissionRequest, Stop, StopFailure,
    SessionEnd) out of the 33 the page lists; the rest deliberately change nothing.
    The payload fields used are session_id, hook_event_name and cwd, documented as
    common to every event. Unknown fields and unknown events are ignored rather
    than rejected, because Claude Code adds them far more often than it removes
    them.
  hold: >-
    a renamed event would silently stop being reported rather than fail loudly:
    the hook would never fire, so there is nothing to notice. Nothing automated can
    see that. Re-read the page when sessions stop changing state.

- name: iterm2-profile-keys
  kind: github-file
  repo: gnachman/iTerm2
  path: api/library/python/iterm2/iterm2/profile.py
  sha: a23c8c3afc1183dd4fd4e2d782da6943b03e29bd
  checked: 2026-09-26
  note: >-
    the profile properties a new tab is created with. Read from iTerm2's own Python
    library rather than guessed: set_use_custom_command writes "Custom Command" and
    takes the string "Yes" or "No" (USE_CUSTOM_COMMAND_ENABLED = "Yes"), and
    set_command writes "Command". Without "Custom Command" the profile ignores
    "Command" and opens a plain shell, so a wrong key here means a tab that opens
    and does nothing.

- name: iterm2-claude-code-integration
  kind: docs
  url: https://iterm2.com/claude-code-integration.html
  checked: 2026-09-26
  note: >-
    iTerm2's own Claude Code integration, which this complements rather than
    extends. Read on 2026-09-26: it installs a cc-status hook into
    ~/.claude/settings.json that reports to iTerm2 over the Python API, and shows
    local sessions in a built-in Session Status toolbelt tool with the same three
    states this uses (working / waiting / idle).
  hold: >-
    the page documents no extension point for a third party to report sessions into
    that built-in tool, and says nothing about remote hosts, SSH or tmux. That is
    why this registers its own panel instead. If iTerm2 ever publishes a way to
    contribute rows, one combined list would be better than two panels — see
    ROADMAP.md.

- name: tmux
  kind: cli
  version: "3.7c"
  minimum: "2.4"
  checked: 2026-09-26
  note: >-
    two behaviours verified against 3.7c on 2026-09-26 rather than assumed.
    `display-message -p -t <pane> '#{session_name}|#{window_id}|#{pane_id}'` prints
    the three fields; for a pane it does not know it exits 0 with empty output,
    which is why the output is what decides success and not the exit status. A lone
    `;` argument separates two tmux commands, which is how select-window and
    attach-session are sent together.
  hold: >-
    the minimum is a judgement, not a measurement: format strings and `-t` are long
    established, but nothing here has been run against an older tmux.

- name: openssh
  kind: cli
  checked: 2026-09-26
  note: >-
    `ssh` is invoked, never linked. Host names are passed through untouched so the
    user's ~/.ssh/config decides the login name, port, jump host and identity.
    BatchMode=yes, ServerAliveInterval and ServerAliveCountMax are set; `-t` is
    requested for attach and withheld for the stream.
  hold: >-
    not verified against a real SSH server by any automated run. The tests drive a
    real subprocess, which is what ssh is to this program, and a stub ssh for the
    stderr path — but no test has connected to a host. See ROADMAP.md entry 1.

- name: iterm2-go
  kind: go-module
  module: github.com/taumatix/iterm2-go
  version: v0.1.0
  checked: 2026-09-26
  note: >-
    the iTerm2 client. Written by the same author as this program, and its own
    UPSTREAM.md records that its connection layer is not verified against a running
    iTerm2 either — so the caveat below is inherited, not independent.
```

## What is and is not verified

**Verified against the real thing.** The tmux behaviours above, in a live tmux
session. The hook recording a status from inside a real tmux pane, end to end
through the built binary. The shell quoting, by running the command this program
builds through a real `/bin/sh` with a stub `tmux` that reports exactly what it
received — which is how the missing quotes around tmux's `;` separator were found,
after a string-level test had blessed the broken version.

**Not verified: anything involving iTerm2, and anything involving SSH.**

- **iTerm2.** No test has talked to iTerm2. The API is off until a human enables it
  in Settings > General > Magic, and this host also denies Apple Events to its
  shell, so even the cookie exchange cannot run. The panel and the tab-opening are
  tested against a fake that records what was asked for. So: the profile keys are
  read from iTerm2's source but never accepted by iTerm2; the toolbelt registration
  is never rendered; no tab has ever been opened.
- **SSH.** No test has connected to a host. `sshd` is reachable on this machine but
  has no key authorised for it, and adding one to the user's `authorized_keys` is
  not this program's business.

Both are `ROADMAP.md` entry 1, and the README says so where a user will see it.
