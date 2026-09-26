# Roadmap

Ordered by how much each entry limits real use. The top is the next thing worth doing unless
there is a reason to say otherwise.

## 1. Run it once, against a real iTerm2 and a real host

Every edge that leaves this machine is covered by a fake. The logic is tested end to end — the
hook records from inside a real tmux pane, the built binary streams over a real pipe, the shell
quoting is checked by a real `/bin/sh` — but:

- **No test has talked to iTerm2.** The API is off until a human enables it in
  Settings → General → Magic, and this host also denies Apple Events to its shell, so even the
  cookie exchange cannot run. So the profile keys are read from iTerm2's source but never
  accepted by iTerm2, the toolbelt registration has never been rendered, and no tab has ever
  been opened.
- **No test has connected over SSH.** `sshd` is reachable on this machine but no key is
  authorised for it, and adding one to the user's `authorized_keys` is not this program's
  business.

What one real run would settle, each of which is a plausible way this is broken today:

- Whether `"Custom Command": "Yes"` plus `"Command"` in `CustomProfileProperties` actually makes
  the new tab run the ssh command, rather than opening a plain shell and ignoring it.
- Whether the web-view tool renders at all in the toolbelt, and whether its JavaScript `fetch`
  to `127.0.0.1` is allowed in iTerm2's web view — a content-security policy there would break
  clicking without breaking rendering.
- Whether `tmux select-window -t @N ';' attach-session -t name` behaves over `ssh -t` the way it
  does locally.
- Whether reading `user.iterm2ClaudeBridge` on every session is fast enough to click through, or
  wants the per-session round trips replacing.

This is the entry to do before any feature, because most of the rest is untestable until the
first real run has happened.

## 2. Two panels is the wrong answer if iTerm2 will take rows

Remote sessions appear in their own toolbelt tool beside iTerm2's Session Status, so a user
watching both local and remote Claude sessions reads two lists. That is a workaround for iTerm2
documenting no extension point, not a design.

Worth asking George Nachman whether the Session Status tool could accept sessions reported by a
third party — a variable, an RPC, anything published. If it can, this becomes a much smaller
program. Until then the second panel is right, because guessing at an internal would break on
every iTerm2 release.

## 3. A session's status goes stale when the hook cannot run

Status is only as good as the last hook that fired. If Claude is killed, the machine sleeps, or
the hook fails, the row keeps saying "working" forever, and nothing distinguishes that from a long
tool call.

Two halves, and the first is cheap: show the age of the last change in the row (already rendered)
and grey out anything older than some threshold. The second is to have the reporter notice that
the process behind a session is gone — which needs the hook to record a pid, and the stream to
check it.

## 4. Nothing verifies that the remote and local builds agree

The two halves talk over a versioned wire format, and the remote one is upgraded by whoever
administers that host. Unknown fields and unknown statuses are ignored rather than rejected, which
is the right default — but a user whose remote build is much older gets quietly reduced function
with nothing saying so.

A version in the stream's first line, and a warning in the panel when it is older than the
watcher expects.

## 5. Discover hosts rather than listing them

`--host` per host, every time. Reading them from a config file, or from `~/.ssh/config` with a
marker, would make watching a dozen machines reasonable. A config file also gives somewhere for
per-host settings — a different profile, a different remote command — which the flags cannot
express today.

## 6. The panel polls itself every five seconds

A full page reload on a timer is the simplest thing that works and it is wasteful: it re-renders
while nothing has changed, and a click landing during a reload is lost (worked around by
cancelling the timer). Server-sent events over the same loopback server would remove both, and
`Registry.Changed()` already exists to drive them.

## Done

- **v0.1.0** — the whole pipeline: hooks recording status with tmux coordinates, a compacted event
  log, a stream that replays and follows, an SSH transport per host with backoff, a registry, a
  toolbelt panel, and click-to-attach that reuses an existing tab. `install-hooks` that shows
  before it writes. Tested against real tmux, the real binary, a real shell and a real HTTP
  server; iTerm2 and SSH by fakes.
