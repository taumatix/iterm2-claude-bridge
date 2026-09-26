# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Clicking a row never opened anything.** Every click answered "that session is no longer being
  reported", whatever the session was doing and whether or not it was under tmux — so v0.1.0's
  central feature did not work at all. A session key joined its host and Claude session id with a
  NUL byte, and `html/template` replaces NUL with U+FFFD when it renders the key into the row's
  `data-key` attribute (the HTML5 tokenizer would replace it anyway). The key posted back was
  therefore never the key the registry held. Keys now percent-encode each half and join them with
  `/`.

  The suite missed it because every test posted a key taken straight from the registry. There is
  now one that reads the key back out of the rendered page with an HTML5 parser before posting it,
  which is the trip a browser actually makes.

- Tab tags had the same ambiguity one layer up: host `a` with tmux session `b/c` and host `a/b`
  with tmux session `c` both tagged a tab `a/b/c`, so a click could reveal the wrong one. Tags use
  the same encoding now. Ordinary names are unchanged, so a tab tagged by v0.1.0 is still found.

### Changed

- `session.Key`, `Session.Key()` and `Event.Key()` return the new encoding. The key is an in-memory
  identifier — it is never written to the event log or sent over the wire — so nothing on disk or
  in flight changes, and the two halves it is built from are untouched.

## [0.1.0] - 2026-09-26

First release. Shows Claude Code sessions running on remote hosts in iTerm2's toolbelt, and opens
a tab that SSHes in and attaches to their tmux session.

### Added

- `install-hooks`, which merges the reporting hooks into `~/.claude/settings.json`. It prints what
  it would change and does nothing until `--write`, backs the file up first, keeps every setting
  it does not understand, and is safe to run twice.
- `hook`, run by Claude Code, recording one status change with the tmux session, window and pane
  it fired in. It always exits 0: a hook runs on the critical path of every tool call, and failing
  it would stop Claude working.
- `stream`, which replays the current state of every session and then follows the log, as
  newline-delimited JSON. This is what the Mac runs over SSH.
- `list`, printing what a host has recorded, for checking the remote side without iTerm2.
- `watch`, which streams each host over `ssh`, keeps a registry, registers a web-view tool in
  iTerm2's toolbelt, and opens or activates a tab on click. Reconnects with capped exponential
  backoff; one unreachable host does not affect the others.
- Three states matching iTerm2's own integration — working, waiting, idle — plus a row for a
  session that has ended, which disappears after a grace period.
- New tabs are tagged with an iTerm2 session variable, so clicking a row twice brings the existing
  tab forward. Read back from iTerm2 rather than remembered, so it survives a restart.
- A compacted event log: replaying it yields current state, and it is rewritten with one event per
  session once it passes 256 KiB.

### Security

- The panel listens on loopback only and requires a random token that iTerm2 receives in the
  tool's URL. Loopback alone is not enough — any web page the user visits can POST to
  `127.0.0.1`.
- Working directories and tmux session names come from another machine, so the panel escapes them
  as HTML and the SSH command shell-quotes them. The quoting is tested by running the built
  command through a real `/bin/sh` with a stub `tmux` that reports what it received.
- Shell quoting works from an allowlist of bytes that need no quoting, rather than a list of
  metacharacters to escape. With a denylist, a byte nobody enumerated is passed through bare and a
  remote-supplied name becomes shell syntax; with an allowlist the same oversight only adds
  redundant quotes. Every byte from 1 to 255 is checked through a real shell.
- The hook command written into `settings.json` is quoted too. Claude Code runs it as a shell line,
  so an unquoted `/Users/First Last/...` — an ordinary macOS home directory — ran a different
  program and the host silently never reported.
- The event log is `0600` inside a `0700` directory.

### Fixed before release

Two bugs that a green local suite hid and CI found, each on one platform only. Both were real, not
flaky tests: the suite passed on the machine that wrote it because of how that machine happened to
schedule, and which test failed was a property of the scheduler rather than of the bug.

- **A status change arriving while a watcher connected was dropped.** `stream` read the log to build
  its replay and then took the file size to decide where to follow from, so a hook firing between
  those two steps was past the replay and behind the offset — reported by neither, with nothing
  recording that anything was missed. `Store.Snapshot` now returns the state and the offset it read
  up to from one read, so following starts exactly where the replay stopped: no gap, and no
  double-reporting either. Found as a macOS CI failure in the corrupt-line test, which had nothing
  to do with corrupt lines.
- **`ssh`'s error output could be discarded exactly when there was some.** `cmd.Wait` closes the pipe
  from `StderrPipe` as soon as the process exits, which raced the goroutine draining it. An
  unreachable host prints `no route to host` and exits immediately — the one case the draining
  exists for — so the diagnostic was lost and the host looked quiet rather than unreachable. Waiting
  for the drain before `Wait` is what fixes it. Found as a Linux CI failure.

### Known limitations

- **Nothing here has talked to a real iTerm2 or a real SSH server.** The logic is covered end to
  end, including the hook recording from inside a real tmux pane, but those two edges are driven
  by fakes: the machine this was built on has iTerm2's API switched off and no key authorised for
  its own `sshd`. `UPSTREAM.md` lists what one real run would settle and `ROADMAP.md` entry 1
  tracks it.
- Remote sessions appear in a second toolbelt panel beside iTerm2's own rather than in one
  combined list, because iTerm2 documents no way for a third party to contribute rows
  (`ROADMAP.md` entry 2).
- A session's status is only as fresh as the last hook that fired. If Claude is killed or the
  machine sleeps, the row keeps saying "working" (`ROADMAP.md` entry 3).

[Unreleased]: https://github.com/taumatix/iterm2-claude-bridge/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/taumatix/iterm2-claude-bridge/releases/tag/v0.1.0
