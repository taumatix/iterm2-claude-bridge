# Roadmap

Ordered by how much each entry limits real use. The top is the next thing worth doing unless
there is a reason to say otherwise.

The numbers are the order and nothing else: inserting an entry renumbers everything below it, so
anything outside this file — the README, a changelog entry, an issue — refers to an entry **by its
title**. A reference by number was wrong twice within a day of being written.

## 1. What two real runs have not reached yet

Two runs on 2026-09-26 took this from "no click was ever served" to "a tab opens, in the right
window, running ssh with the user's environment". Between them they settled six things that had
been listed here as plausible ways the program was broken: the toolbelt tool registers and
renders, the web view runs the page's JavaScript, its `fetch` to `127.0.0.1` is not blocked, the
token in the URL is accepted, `CreateTab` with `"Custom Command": "Yes"` runs the ssh command
rather than a plain shell, and `ssh` itself runs from a tab iTerm2 spawned.

What no run has reported either way:

- Whether v0.2.0's reconnect works against a real iTerm2 restart: how soon `Conn.Done()` fires
  when iTerm2 quits, whether the AppleScript cookie request succeeds from the process `watch` runs
  in, and whether the re-registered panel reappears in the toolbelt without being revealed.
- Whether `tmux select-window -t @N ';' attach-session -t name` behaves over `ssh -t` the way it
  does locally. A third run reached it and it failed, but for a reason on this side of the wire:
  the `;` lost its quoting to a shell layer the tests did not know about, fixed in v0.1.3. Whether
  it then lands on the pane Claude is in is still unreported.
- Whether reading `user.iterm2ClaudeBridge` on every session is fast enough to click through, or
  wants the per-session round trips replacing.
- Whether the three properties added on 2026-09-26 are accepted: `"Run Command In Login Shell"`,
  `window_id` from a `FocusRequest`, and the `\(` refusal.

## 2. Escaping, rather than refusing, a command iTerm2 will evaluate

A profile's `Command` is an interpolated string — `computeCommandForProfile` evaluates it with
`iTermExpressionEvaluator`, side effects allowed, before `-componentsInShellCommand` splits it
into argv. A tmux session name comes from another machine and lands in that string, so a name
containing `\(` runs an iTerm2 expression on the Mac. Shell quoting happens a layer too late.

Today that command is refused, which fails closed and costs a user with such a name their click.
Escaping it properly means establishing two things that can only be established against a running
iTerm2, because the layers compose:

- Whether doubling a backslash neutralises the expression opener as `iTermSwiftyStringParser.m`
  reads like it should (`\` then a character that is not `(` returns to the literal state), and
  whether the literal that survives evaluation still carries both backslashes.
- What `-componentsInShellCommand` then does with them, since it has its own backslash handling
  and the two must not cancel to something different from what was meant.

The repo's own history says how to settle it: the missing quotes around tmux's `;` were found by
running the built command through a real `/bin/sh` with a stub `tmux`, after a string-level test
had blessed the broken version. The equivalent here needs iTerm2 in the loop, which is why this
sits behind the entry above rather than in front of it.

## 3. Only a tab this program opened can be found again

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

## 4. The chat / diff / code-review peers

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

Half of that is now answered by the documentation. Read on 2026-09-27, the Workgroups page says
"Commands and file commands support iTerm2's interpolated string syntax, `\(name)`", and lists
`gitBase`, `file`, `codeReviewPrompt` and `codeReviewSystemPrompt` as "the variables most useful
in Workgroups". So a Peer Command is interpolated. The page still does not say which scope it is
evaluated in, so whether the parent session's `user.` variables resolve there is unanswered.

Answer that question against a real iTerm2 first. It is one experiment and it decides whether this
entry is small or is a request to George Nachman.

## 5. Two panels is the wrong answer if iTerm2 will take rows

Remote sessions appear in their own toolbelt tool beside iTerm2's Session Status, so a user
watching both local and remote Claude sessions reads two lists. That is a workaround for iTerm2
documenting no extension point, not a design.

Worth asking George Nachman whether the Session Status tool could accept sessions reported by a
third party — a variable, an RPC, anything published. If it can, this becomes a much smaller
program. Until then the second panel is right, because guessing at an internal would break on
every iTerm2 release.

## 6. A session's status goes stale when the hook cannot run

Status is only as good as the last hook that fired. v0.3.0 marks a *working* row stale after ten
quiet minutes, since a working session reports on every tool call. That leaves two gaps:

- A *waiting* or *idle* session whose Claude was killed looks exactly like one resting, because
  resting is silent too. Telling them apart needs the reporter to notice the process is gone: the
  hook records Claude's pid, and the stream checks it and reports the session `gone`. A pid alone
  can be reused, so it needs the process start time with it.
- A model turn that calls no tool for over ten minutes (a long think, a slow API) is marked stale
  while alive. `--stale-after` is the workaround. A `UserPromptSubmit` or periodic heartbeat from
  the hook would make the threshold safe to shorten; the pid check above would make it unneeded.

## 7. Nothing verifies that the remote and local builds agree

The two halves talk over a versioned wire format, and the remote one is upgraded by whoever
administers that host. Unknown fields and unknown statuses are ignored rather than rejected, which
is the right default — but a user whose remote build is much older gets quietly reduced function
with nothing saying so.

This stopped being hypothetical in v0.1.3: a remote that does not send `tmux.binary` leaves the
Mac guessing `tmux`, which is the `command not found: tmux` that release fixed. The user upgrades
the Mac, sees no change, and has nothing telling them the other half is the stale one.

A version in the stream's first line, and a warning in the panel when it is older than the
watcher expects.

## 8. Discover hosts rather than listing them

`--host` per host, every time. Reading them from a config file, or from `~/.ssh/config` with a
marker, would make watching a dozen machines reasonable. A config file also gives somewhere for
per-host settings — a different profile, a different remote command — which the flags cannot
express today.

## 9. The panel polls itself every five seconds

A full page reload on a timer is the simplest thing that works and it is wasteful: it re-renders
while nothing has changed, and a click landing during a reload is lost (worked around by
cancelling the timer). Server-sent events over the same loopback server would remove both, and
`Registry.Changed()` already exists to drive them.

## 10. A reconnect that keeps failing says so only in the log

Since v0.2.0 `watch` reconnects when iTerm2 comes back. If it never can — the API was switched off,
or macOS denies the AppleScript cookie request a reconnect needs — it retries every 30s for ever,
and the failures are logged at debug level. The panel is gone from the toolbelt, so the user sees
nothing at all, which is the original problem again in a narrower case.

The first failed attempt is the moment to say something at warning level, once, naming the likely
cause (the Automation permission in System Settings, or the API setting) rather than repeating it
every 30s. Whether to go further, a macOS notification, needs a real run to see what the failure
actually looks like from a process iTerm2 did not launch.

## Done

- **v0.3.0**: a working session that has stopped reporting is marked stale, after ten minutes by
  default (`watch --stale-after`). It is tested by rendering the real panel over HTTP; how it looks
  in iTerm2's toolbelt has not been seen, which belongs to entry 1.

- **v0.2.0**: `watch` survives iTerm2 restarting. `bridge.Link` holds the current connection,
  reconnects with a doubling wait (1s to 30s), registers the panel again on each new connection,
  and answers `ErrITermUnavailable` in between. It is tested over a real unix socket against a
  stand-in iTerm2 that is dropped and brought back. The real-iTerm2 check is part of entry 1.

- **v0.1.3** — what reaches tmux is what was meant. There are two shells between the panel and
  tmux, and the quoting test only knew about the second: iTerm2 parses a profile's Command before
  ssh exists, so the quoting meant for the remote shell was consumed locally. That broke the `;`
  separator — `command not found: attach-session` — and, worse, expanded a remote-chosen session
  name on the Mac, so `$(…)` in one ran here. Both layers are quoted now and the test starts from
  the string iTerm2 is given. The reporter also sends the absolute path of its tmux, because ssh
  runs the remote command non-interactively and that PATH commonly cannot find one.

- **v0.1.2** — the tab opens where the user is and runs ssh with their own environment. It was
  opening a new window, because `CreateTab` does that without a window id; it now goes in the
  window a `FocusRequest` reports as key. And iTerm2 exec'd the ssh command directly, so no
  dotfile ran and a `SSH_AUTH_SOCK` from `~/.zshrc` was missing; `"Run Command In Login Shell"`
  is iTerm2's own answer to that. Reading its source for those two found a third thing: a
  profile's Command is expression-evaluated before it is split into argv, so a remote tmux
  session name containing `\(` ran an iTerm2 expression. Refused for now.

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
