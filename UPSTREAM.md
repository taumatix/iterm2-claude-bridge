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
  checked: 2026-09-27
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
  checked: 2026-09-27
  note: >-
    the profile properties a new tab is created with. Read from iTerm2's own Python
    library rather than guessed: set_use_custom_command writes "Custom Command" and
    takes the string "Yes" or "No" (USE_CUSTOM_COMMAND_ENABLED = "Yes"), and
    set_command writes "Command". Without "Custom Command" the profile ignores
    "Command" and opens a plain shell, so a wrong key here means a tab that opens
    and does nothing.
  hold: >-
    the Python library is behind the application. profile.py documents "Custom
    Command" as a Yes/No flag; ITAddressBookMgr.m (below) shows the application
    comparing it against five values, of which "Yes" is one. It also carries no
    setter for "Run Command In Login Shell", which the application has and this
    program needs. Prefer the Objective-C source for a key this depends on.

- name: iterm2-command-execution
  kind: github-file
  repo: gnachman/iTerm2
  path: sources/Settings/Profiles/ITAddressBookMgr.m
  ref: master
  checked: 2026-09-27
  note: >-
    how a profile's Command becomes a running program, read on 2026-09-26 because
    two v0.1.1 bugs were in it. KEY_CUSTOM_COMMAND is "Custom Command" and
    kProfilePreferenceCommandTypeCustomValue is "Yes"; KEY_COMMAND_LINE is
    "Command"; KEY_RUN_COMMAND_IN_LOGIN_SHELL is "Run Command In Login Shell",
    a boolean defaulting to NO (iTermProfilePreferences.m). With it set,
    bookmarkCommandSwiftyString wraps the command as
    `/usr/bin/login -f[q]pl <user> ShellLauncher --launch_shell - -i -c <cmd>`,
    which is what makes the user's dotfiles run before ssh — iTerm2's own comment
    on the adjacent ssh path names a custom SSH_AUTH_SOCK as the case it is for.
    computeCommandForProfile then evaluates the result as an interpolated string
    with iTermExpressionEvaluator, side effects allowed, and the result is split
    into argv by -componentsInShellCommand.
  hold: >-
    the evaluation step is a trust boundary this program did not know it had. A
    tmux session name arrives from another machine and lands in that string, and
    shell quoting happens a layer too late to help. iTermSwiftyStringParser.m
    starts an expression at a backslash followed by "(" and at nothing else, so
    openTab refuses a command containing `\(` rather than trying to escape it —
    escaping correctly would mean knowing how the expression layer and the shell
    tokenizer compose, which cannot be tested from here. See ROADMAP.md.

- name: iterm2-focus
  kind: proto-message
  repo: gnachman/iTerm2
  path: proto/api.proto
  ref: master
  checked: 2026-09-27
  note: >-
    which window a new tab belongs in. CreateTabRequest.window_id is optional and
    a new window is created without it, which is why every click in v0.1.1 opened
    a window. FocusRequest is empty and FocusResponse returns
    FocusChangedNotification values that "completely describe the state of every
    tab and window and the application itself"; a Window notification carries
    TERMINAL_WINDOW_BECAME_KEY, TERMINAL_WINDOW_IS_CURRENT or
    TERMINAL_WINDOW_RESIGNED_KEY. The key window is the one whose toolbelt was
    clicked, so it is where the tab goes; current is the fallback when a
    non-terminal window has focus.

- name: iterm2-claude-code-integration
  kind: docs
  url: https://iterm2.com/claude-code-integration.html
  checked: 2026-09-27
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
  checked: 2026-09-27
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
    Settings and reached through the profile a tab is opened with. Re-read on
    2026-09-27, including api.proto at 5ed491d (still no match): the page now
    says Peer commands support interpolated strings, \(name), and lists gitBase,
    file, codeReviewPrompt and codeReviewSystemPrompt. It does not say which scope
    they are evaluated in, so whether the parent session's user. variables
    resolve — what decides whether one Workgroup definition can serve every
    remote host — is still open and has not been tried. See ROADMAP.md.

- name: tmux
  kind: cli
  version: "3.7c"
  minimum: "2.4"
  checked: 2026-09-27
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
  version: "10.3p1"
  checked: 2026-09-27
  note: >-
    `ssh` is invoked, never linked. Host names are passed through untouched so the
    user's ~/.ssh/config decides the login name, port, jump host and identity.
    BatchMode=yes, ServerAliveInterval and ServerAliveCountMax are set; `-t` is
    requested for attach and withheld for the stream. Two behaviours read from
    ssh(1) on 2026-09-26 after a user hit both: "If supplied, the arguments will
    be appended to the command, separated by spaces, before it is sent to the
    server to be executed" — so a `;` arriving as its own argument reaches the
    remote shell bare, whatever quoting put it there — and the server "executes
    the given command in a non-interactive session", whose PATH commonly lacks a
    tmux under /opt/homebrew/bin, which is why the reporter sends the path it
    resolved.
  hold: >-
    not verified against a real SSH server by any automated run. The tests drive a
    real subprocess, which is what ssh is to this program, and a stub ssh for the
    stderr path — but no test has connected to a host. See ROADMAP.md, "What two
    real runs have not reached yet".

- name: iterm2-go
  kind: go-module
  module: github.com/taumatix/iterm2-go
  version: v0.2.0
  checked: 2026-10-01
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

That quoting check ran against **one** shell until 2026-09-26, and there are two.
iTerm2 parses a profile's Command before ssh exists, so the quoting meant for the
remote shell was consumed locally and a session name containing `$(…)` ran on the
Mac. The check now starts from the string iTerm2 is given and goes through both —
the local shell, then a stub ssh that joins its arguments as ssh(1) says it does,
then the remote shell. Starting a test one layer in is how a real injection sat
under a green suite for three releases.

**Established by one real run, not by a test.** A user ran v0.1.0 against a real
iTerm2 on 2026-09-26 and reported that clicking a row said "that session is no
longer being reported". That sentence can only be produced by the panel's own
error box, after a `POST /open` was served and answered 404 — so reaching it
means the toolbelt registration rendered, the web view ran the page's
JavaScript, its `fetch` to `127.0.0.1` was allowed, the token was accepted, and
the row carried a `data-key` and was not disabled. Four of the things ROADMAP
listed as unknown are therefore answered, and the fifth — the key round trip —
was broken and is fixed in 0.1.1.

This is inference from one reported message, not a run anyone here observed or
can repeat. It is recorded because the alternative is to keep calling those
parts unknown when something is now known about them, not because it is
equivalent to a test.

**A second real run, on 2026-09-26, opened a tab.** The v0.1.1 fix let a click
through, and the user reported the two things that were then wrong: the tab
opened in a *new window*, and `ssh` ran without the environment their dotfiles
build, so `SSH_AUTH_SOCK` was unset. Both reports are evidence that the rest
works: `CreateTab` is accepted, `"Custom Command": "Yes"` with `"Command"` does
make the new tab run the ssh command rather than a plain shell, and `ssh` itself
runs. That settles two more of that entry's unknowns, and again by inference
from what was reported rather than from a run observed here.

**Still not verified.**

- **iTerm2.** No test has talked to iTerm2. The API is off until a human enables
  it in Settings > General > Magic, and this host also denies Apple Events to its
  shell, so even the cookie exchange cannot run here. The panel and the
  tab-opening are tested against a fake that records what was asked for. Three
  things added on 2026-09-26 have never been accepted by iTerm2: the
  `"Run Command In Login Shell"` property, `CreateTabRequest.window_id` carrying
  a window read from a `FocusRequest`, and the refusal of a command containing
  `\(`. The first two are read from iTerm2's source, the third from its parser.
- **tmux over ssh.** Whether `tmux select-window ';' attach-session` behaves over
  `ssh -t` the way it does locally is still unreported either way.
- **SSH.** No test has connected to a host. `sshd` is reachable on this machine but
  has no key authorised for it, and adding one to the user's `authorized_keys` is
  not this program's business.

Both are `ROADMAP.md`, *What two real runs have not reached yet*, and the README
says so where a user will see it.
