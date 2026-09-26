# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
