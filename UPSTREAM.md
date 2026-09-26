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
    states this uses (working / waiting / idle). It also ships a Workgroup, which
    on entry creates two Peers beside the Claude session — "Chat is your main
    Claude conversation. Diff shows a side-by-side view of your working-tree
    changes. Code Review is a dedicated session for reviewing changes." The
    integration uses the Python API to add Code Review findings to the
    Workgroup's shared Clippings panel.
  hold: >-
    the page documents no extension point for a third party to report sessions into
    that built-in tool, and says nothing about remote hosts, SSH or tmux. That is
    why this registers its own panel instead. If iTerm2 ever publishes a way to
    contribute rows, one combined list would be better than two panels — see
    ROADMAP.md.

- name: iterm2-workgroups
  kind: docs
  url: https://iterm2.com/documentation-workgroups.html
  checked: 2026-09-26
  note: >-
    how the chat / diff / code-review panes of iTerm2's integration are built. A
    Workgroup is "a set of related sessions that iTerm2 builds from a single one",
    configured in Settings > Arrangements > Workgroups. Each Peer carries a
    Profile, a Mode (Regular, Diff or Code Review), a Command, a Name and an
    optional Shortcut, and its command runs when the Workgroup is entered.
  hold: >-
    no API. proto/api.proto at gnachman/iTerm2 master on 2026-09-26 contains no
    message, field or enum matching "workgroup" or "peer", so a Workgroup can be
    neither created nor entered from this program — only defined by a human in
    Settings and reached through the profile a tab is opened with. Whether a Peer
    Command may be an interpolated string reading the parent session's user.
    variables is the open question that decides whether one Workgroup definition
    can serve every remote host; the page does not say, and it has not been tried.
    See ROADMAP.md.

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

**Established by one real run, not by a test.** A user ran v0.1.0 against a real
iTerm2 on 2026-09-26 and reported that clicking a row said "that session is no
longer being reported". That sentence can only be produced by the panel's own
error box, after a `POST /open` was served and answered 404 — so reaching it
means the toolbelt registration rendered, the web view ran the page's
JavaScript, its `fetch` to `127.0.0.1` was allowed, the token was accepted, and
the row carried a `data-key` and was not disabled. Four of the things ROADMAP
entry 1 listed as unknown are therefore answered, and the fifth — the key round
trip — was broken and is fixed in Unreleased.

This is inference from one reported message, not a run anyone here observed or
can repeat. It is recorded because the alternative is to keep calling those
parts unknown when something is now known about them, not because it is
equivalent to a test.

**Still not verified: opening a tab, and anything involving SSH.**

- **iTerm2.** No test has talked to iTerm2, and the click that would have created
  a tab never got past the lookup, so nothing has yet exercised `CreateTab`. The
  API is off until a human enables it in Settings > General > Magic, and this
  host also denies Apple Events to its shell, so even the cookie exchange cannot
  run here. The panel and the tab-opening are tested against a fake that records
  what was asked for. So the profile keys — `"Custom Command": "Yes"` and
  `"Command"` — are read from iTerm2's source and have still never been accepted
  by iTerm2, and no tab has ever been opened.
- **SSH.** No test has connected to a host. `sshd` is reachable on this machine but
  has no key authorised for it, and adding one to the user's `authorized_keys` is
  not this program's business.

Both are `ROADMAP.md` entry 1, and the README says so where a user will see it.
