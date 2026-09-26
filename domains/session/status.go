// Package session is the vocabulary both halves of the bridge speak: what a
// Claude Code session is, what state it is in, and how a hook event changes
// that.
//
// It is deliberately free of transport, tmux and iTerm2 concerns, because it is
// serialised across an SSH connection between two builds of this program that
// may not be the same version.
package session

import (
	"fmt"
	"slices"
)

// Status is what a session is doing, using the three states iTerm2's own
// Session Status tool shows so the two panels read the same way: working while
// Claude is running a prompt or a tool, waiting when it needs input, idle when
// it has finished.
type Status string

const (
	// StatusWorking means Claude is running a prompt or a tool.
	StatusWorking Status = "working"

	// StatusWaiting means Claude needs the user, either for input or for a
	// permission decision.
	StatusWaiting Status = "waiting"

	// StatusIdle means Claude has finished responding.
	StatusIdle Status = "idle"

	// StatusGone means the session has ended. It is reported rather than simply
	// forgotten so the far side can remove its row instead of waiting for a
	// timeout.
	StatusGone Status = "gone"
)

// Statuses lists every status, in the order a panel should group them: the ones
// wanting attention first.
func Statuses() []Status {
	return []Status{StatusWaiting, StatusWorking, StatusIdle, StatusGone}
}

// Valid reports whether s is a status this build knows.
//
// An unknown status arriving from a newer remote build is not an error to
// reject: see [StatusFromHookEvent] and the note on forward compatibility in
// Event.
func (s Status) Valid() bool {
	return slices.Contains(Statuses(), s)
}

// hookEventStatus maps Claude Code's hook event names onto the three states.
//
// The names and their meanings are from Claude Code's hooks reference
// (https://code.claude.com/docs/en/hooks), not from recall — each comment below
// is that page's description of when the event fires. Only the events that
// change what a *watcher* should see are listed; the rest are deliberately
// absent, because a hook firing is not the same as a status changing, and
// mapping everything would make every session flicker.
var hookEventStatus = map[string]Status{
	// "When a session begins or resumes" — it exists, and is not yet busy.
	"SessionStart": StatusIdle,

	// "When you submit a prompt, before Claude processes it".
	"UserPromptSubmit": StatusWorking,
	// "Before a tool call executes" / "After a tool call succeeds". Both are
	// reported because a long tool run should not look idle, and because a hook
	// can be missed.
	"PreToolUse":  StatusWorking,
	"PostToolUse": StatusWorking,

	// "When Claude Code sends a notification" — iTerm2's integration treats this
	// as the signal that Claude needs you, and so does this.
	"Notification": StatusWaiting,
	// "When a tool call needs a permission decision" — waiting on a human by
	// definition.
	"PermissionRequest": StatusWaiting,

	// "When Claude finishes responding".
	"Stop": StatusIdle,
	// "When the turn ends due to an API error" — finished, just not happily. The
	// distinction is worth keeping one day; today it is not a fourth colour.
	"StopFailure": StatusIdle,

	// "When a session terminates".
	"SessionEnd": StatusGone,
}

// StatusFromHookEvent maps a Claude Code hook event name to a status.
//
// ok is false for an event that should not change what a watcher sees, which is
// most of them. The caller should do nothing rather than guess: an unrecognised
// event is far more likely to be one this build has no opinion about than a
// state change it should invent.
func StatusFromHookEvent(hookEventName string) (status Status, ok bool) {
	status, ok = hookEventStatus[hookEventName]
	return status, ok
}

// HookEventsReported lists the hook event names this build acts on, so the
// installer can write exactly those into settings.json rather than every event.
func HookEventsReported() []string {
	names := make([]string, 0, len(hookEventStatus))
	for name := range hookEventStatus {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ParseStatus converts a wire value to a Status.
func ParseStatus(s string) (Status, error) {
	status := Status(s)
	if !status.Valid() {
		return "", fmt.Errorf("session: %q is not a known status", s)
	}
	return status, nil
}
