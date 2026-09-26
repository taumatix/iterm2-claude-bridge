# Roadmap

Ordered by how much each entry limits real use. The top is the next thing worth doing unless
there is a reason to say otherwise.

## 1. A tab has still never been opened

The first real run, on 2026-09-26, got as far as the click and no further: every click answered
"that session is no longer being reported", because the row's key could not survive being an HTML
attribute. That is fixed, and reaching that error proved four things that had been unknown — the
toolbelt tool registers and renders, the web view runs the page's JavaScript, its `fetch` to
`127.0.0.1` is not blocked by a content-security policy, and the token in the URL is accepted.

What is still unknown is everything past the lookup:

- Whether `"Custom Command": "Yes"` plus `"Command"` in `CustomProfileProperties` actually makes
  the new tab run the ssh command, rather than opening a plain shell and ignoring it.
- Whether `tmux select-window -t @N ';' attach-session -t name` behaves over `ssh -t` the way it
  does locally.
- Whether reading `user.iterm2ClaudeBridge` on every session is fast enough to click through, or
  wants the per-session round trips replacing.
- Whether `ssh` works at all from a tab iTerm2 spawned: no automated test has connected to a host,
  because `sshd` here has no key authorised for it.

Nothing else on this list can be trusted until a click has opened a working tab, so this stays at
the top.

## 2. Only a tab this program opened can be found again

Clicking a row finds an existing tab by a variable this program wrote on tabs it created. Two
cases it therefore cannot see, both of which a user hits on the first day:

- A tmux session the user attached to by hand, in a tab they opened themselves. The click opens a
  second tab attached to the same session rather than switching to the one in front of them.
- A Claude session not running under tmux. There is nothing to attach to, so the row is deliberately
  dead — but if a tab showing that session is already open, switching to it is exactly right, and
  is the only thing that row could usefully do.

Both need the same thing: a tab that says which Claude session it is showing, whoever opened it.
The mechanism to try is `OSC 1337 ; SetUserVar=name=<base64> ST` emitted by the remote hook, which
already runs inside the pane. Then `findTagged` looks for a session id rather than for this
program's own tag, and both cases fall out.

Two costs to establish before building it:

- tmux's `allow-passthrough` is **off** by default (verified on tmux 3.7c, 2026-09-26: `show -gpA`
  reports `allow-passthrough* off`), and the sequence has to be wrapped as `\ePtmux;...\e\\` to get
  through. So this asks the user to change a tmux setting, which the README must say plainly.
- Whether the sequence survives `ssh` and reaches iTerm2 as a session variable at all has not been
  tried. iTerm2's escape-code documentation says nothing about tmux or ssh.

## 3. The chat / diff / code-review peers

iTerm2's own integration puts three sessions one click apart — Chat, Diff and Code Review — and a
remote session gets none of them. That is the gap a user notices immediately after clicking works.

The mechanism is iTerm2's **Workgroups** (Settings > Arrangements > Workgroups): a Workgroup builds
a set of related sessions from one, and each Peer carries a Profile, a Mode — Regular, **Diff** or
**Code Review** — a Command, a Name and a shortcut. The two modes needed are named modes iTerm2
already has, so this is configuration rather than a new panel.

It is not reachable from here, though: `proto/api.proto` at gnachman/iTerm2 master on 2026-09-26
contains no message, field or enum matching "workgroup" or "peer", so this program can neither
create a Workgroup nor enter one. What it can do is open its tab with a profile that has one
attached, and ship the definition for a user to add.

The question that decides the shape is whether a Peer's Command may be an interpolated string
reading the parent session's `user.` variables. If it can, one Workgroup definition serves every
host: the bridge sets `user.iterm2ClaudeBridgeHost` and `…Cwd` on the tab it creates, and the Diff
peer runs `ssh \(user.iterm2ClaudeBridgeHost) -t cd \(user.iterm2ClaudeBridgeCwd) && git diff`.
If it cannot, the command is fixed at definition time and a user needs one Workgroup per host,
which is a much worse thing to ask for and probably means asking iTerm2 upstream instead.

Answer that question against a real iTerm2 first. It is one experiment and it decides whether this
entry is small or is a request to George Nachman.

## 4. Two panels is the wrong answer if iTerm2 will take rows

Remote sessions appear in their own toolbelt tool beside iTerm2's Session Status, so a user
watching both local and remote Claude sessions reads two lists. That is a workaround for iTerm2
documenting no extension point, not a design.

Worth asking George Nachman whether the Session Status tool could accept sessions reported by a
third party — a variable, an RPC, anything published. If it can, this becomes a much smaller
program. Until then the second panel is right, because guessing at an internal would break on
every iTerm2 release.

## 5. A session's status goes stale when the hook cannot run

Status is only as good as the last hook that fired. If Claude is killed, the machine sleeps, or
the hook fails, the row keeps saying "working" forever, and nothing distinguishes that from a long
tool call.

Two halves, and the first is cheap: show the age of the last change in the row (already rendered)
and grey out anything older than some threshold. The second is to have the reporter notice that
the process behind a session is gone — which needs the hook to record a pid, and the stream to
check it.

## 6. Nothing verifies that the remote and local builds agree

The two halves talk over a versioned wire format, and the remote one is upgraded by whoever
administers that host. Unknown fields and unknown statuses are ignored rather than rejected, which
is the right default — but a user whose remote build is much older gets quietly reduced function
with nothing saying so.

A version in the stream's first line, and a warning in the panel when it is older than the
watcher expects.

## 7. Discover hosts rather than listing them

`--host` per host, every time. Reading them from a config file, or from `~/.ssh/config` with a
marker, would make watching a dozen machines reasonable. A config file also gives somewhere for
per-host settings — a different profile, a different remote command — which the flags cannot
express today.

## 8. The panel polls itself every five seconds

A full page reload on a timer is the simplest thing that works and it is wasteful: it re-renders
while nothing has changed, and a click landing during a reload is lost (worked around by
cancelling the timer). Server-sent events over the same loopback server would remove both, and
`Registry.Changed()` already exists to drive them.

## Done

- **v0.1.1** — clicking a row works. v0.1.0 built a session key by joining its two halves with a
  NUL byte, which `html/template` turns into U+FFFD on the way into the row's attribute, so the
  key posted back never matched and every click answered "that session is no longer being
  reported". Keys percent-encode each half now, tab tags with them, and a test reads the key back
  out of the rendered page with an HTML5 parser instead of taking it from the registry.

- **v0.1.0** — the whole pipeline: hooks recording status with tmux coordinates, a compacted event
  log, a stream that replays and follows, an SSH transport per host with backoff, a registry, a
  toolbelt panel, and click-to-attach that reuses an existing tab. `install-hooks` that shows
  before it writes. Tested against real tmux, the real binary, a real shell and a real HTTP
  server; iTerm2 and SSH by fakes.
