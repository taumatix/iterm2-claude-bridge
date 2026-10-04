package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Event is one status change, as the remote half writes it and the local half
// reads it.
//
// # Forward compatibility
//
// This crosses an SSH connection between two installs of this program that are
// not necessarily the same version — the remote one is upgraded by whoever
// administers that host, which may not be whoever runs the local one. So:
//
//   - Fields are only ever added, never renamed or repurposed.
//   - An unknown field is ignored rather than rejected, which is what
//     encoding/json does by default and why [DecodeEvent] does not set
//     DisallowUnknownFields.
//   - An unknown Status is kept as-is rather than rejected, so a newer remote
//     reporting a fourth state produces a row this build renders plainly instead
//     of a dropped session. Only a missing key makes an event unusable.
type Event struct {
	// Host is the name the local side used to reach this machine — the alias from
	// the user's ssh config, not the hostname the remote knows itself by, because
	// that alias is what has to go back into an ssh command. The remote half
	// leaves it empty and the local half fills it in on receipt.
	Host string `json:"host,omitempty"`

	// SessionID is Claude Code's own session id, from the hook payload. It
	// identifies the Claude session, not the tmux one: several Claude sessions can
	// share a tmux window.
	SessionID string `json:"session_id"`

	Status Status `json:"status"`

	// At is when the remote half saw the event, in UTC. Clocks between the two
	// machines need not agree, so this orders events from one host and is not
	// compared across hosts.
	At time.Time `json:"at"`

	// Tmux locates the session so the local half can attach to it. It is zero when
	// Claude is not running under tmux, in which case there is nothing to attach
	// to and the row is informational.
	Tmux TmuxTarget `json:"tmux,omitempty"`

	// Cwd is Claude's working directory, shown in the panel because it is how a
	// human tells two sessions on one host apart.
	Cwd string `json:"cwd,omitempty"`

	// HookEvent is the Claude Code event that produced this, kept for diagnosis:
	// when a session is in an unexpected state, the question is always which hook
	// last fired.
	HookEvent string `json:"hook_event,omitempty"`

	// Process is the Claude process the hook ran for, so the remote half can
	// notice it has gone without a SessionEnd hook. Nil from a build before it
	// was recorded, or when the hook could not tell.
	Process *Process `json:"process,omitempty"`
}

// Process identifies one running process across time.
type Process struct {
	PID int `json:"pid"`

	// Started is the process's start time as the host's ps reports it. A pid is
	// reused once its process exits, so a pid alone cannot say the same process
	// is still running; a pid with the same start time can.
	Started string `json:"started"`
}

// TmuxTarget is enough to attach to the right place.
type TmuxTarget struct {
	// Session is the tmux session name.
	Session string `json:"session,omitempty"`

	// Window and Pane are the ids tmux assigns, including their sigils ("@3",
	// "%7"), so they can be passed straight back to tmux as a target.
	Window string `json:"window,omitempty"`
	Pane   string `json:"pane,omitempty"`

	// Binary is the absolute path of the tmux that owns this pane, as resolved on
	// the machine it runs on.
	//
	// It is carried because the two halves see different PATHs. ssh runs the
	// attach command in a non-interactive session, whose PATH commonly lacks a
	// tmux installed under /opt/homebrew/bin or /usr/local/bin — the remote shell
	// then answers "command not found: tmux". The reporter runs inside the pane,
	// where tmux is on the PATH by definition, so it is the half that knows.
	//
	// Empty when it could not be resolved, and from a remote build that predates
	// the field; the local half then falls back to the bare name.
	Binary string `json:"binary,omitempty"`
}

// Zero reports whether no tmux session was found.
func (t TmuxTarget) Zero() bool {
	return t.Session == ""
}

// Key identifies a session across events. Claude's session id is unique on its
// own, but not between hosts once several are watched.
func (e Event) Key() string {
	return Key(e.Host, e.SessionID)
}

// Key builds the identifier for a session on a host.
//
// The two halves are percent-encoded and joined with "/", which is not
// decoration. The key is rendered into an HTML attribute in the panel and
// posted back when a row is clicked, so it has to survive that trip. v0.1.0
// joined them with a NUL byte — chosen because neither half can contain one —
// and html/template replaces NUL with U+FFFD, as does the HTML5 tokenizer. The
// key coming back therefore never matched the one the registry held, and every
// click answered "that session is no longer being reported".
//
// url.QueryEscape leaves only ASCII that an attribute, a form body and a URL
// all carry unchanged, and escapes "/" itself, so the join stays unambiguous
// whatever a host alias or a session id contains.
func Key(host, sessionID string) string {
	return url.QueryEscape(host) + "/" + url.QueryEscape(sessionID)
}

// Validate reports whether the event carries the fields everything else relies
// on. It deliberately does not reject an unknown Status — see the note on Event.
func (e Event) Validate() error {
	var problems []string
	if e.SessionID == "" {
		problems = append(problems, "no session_id")
	}
	if e.Status == "" {
		problems = append(problems, "no status")
	}
	if e.At.IsZero() {
		problems = append(problems, "no at")
	}
	if len(problems) > 0 {
		return fmt.Errorf("session: unusable event: %s", strings.Join(problems, ", "))
	}
	return nil
}

// ErrSkipLine reports a line that is not an event and should be passed over
// rather than ending the stream: a blank line, or a comment.
var ErrSkipLine = errors.New("session: not an event line")

// DecodeEvent parses one line of the newline-delimited stream.
//
// A blank line yields [ErrSkipLine]. Anything else that will not parse is an
// error the caller should report and carry on from: one corrupt line — a partial
// write, or a stray message on the remote's stdout — must not end a watch that
// is otherwise working.
func DecodeEvent(line []byte) (Event, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return Event{}, ErrSkipLine
	}

	var e Event
	if err := json.Unmarshal([]byte(trimmed), &e); err != nil {
		return Event{}, fmt.Errorf("session: decoding event: %w", err)
	}
	if err := e.Validate(); err != nil {
		return Event{}, err
	}
	return e, nil
}

// Encode renders the event as one line, newline included, ready to append to
// the stream.
func (e Event) Encode() ([]byte, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("session: encoding event: %w", err)
	}
	return append(body, '\n'), nil
}
