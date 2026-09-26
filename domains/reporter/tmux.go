// Package reporter is the half of the bridge that runs on the machine where
// Claude Code runs.
//
// It is invoked two ways: as a Claude Code hook, which records one status
// change, and as a stream, which replays what it has recorded and then follows
// it. Nothing here knows about iTerm2 — it only writes the vocabulary in
// domains/session to a file and reads it back.
package reporter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// tmuxFormat asks tmux for the three fields that locate a pane.
//
// Verified against tmux 3.7c: `tmux display-message -p -t %0
// '#{session_name}|#{window_id}|#{pane_id}'` prints `probe|@0|%0`. The ids come
// back with their sigils, which is what tmux wants back as a target.
const tmuxFormat = "#{session_name}|#{window_id}|#{pane_id}"

// CommandRunner runs an external command. It exists so tmux resolution can be
// tested without tmux, though the tests that matter here use the real thing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
}

// ExecRunner runs commands with os/exec.
type ExecRunner struct{}

// Run executes name with args and returns its trimmed standard output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("%s: %s", name, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// TmuxResolver finds which tmux pane the current process is running in.
type TmuxResolver struct {
	Runner CommandRunner

	// Env reads an environment variable. Defaults to os.Getenv.
	Env func(string) string
}

// NewTmuxResolver returns a resolver driving the real tmux.
func NewTmuxResolver() *TmuxResolver {
	return &TmuxResolver{Runner: ExecRunner{}, Env: os.Getenv}
}

func (r *TmuxResolver) env(name string) string {
	if r.Env != nil {
		return r.Env(name)
	}
	return os.Getenv(name)
}

// Resolve returns the tmux pane this process is in.
//
// It returns a zero target and no error when the process is not under tmux:
// running Claude outside tmux is an ordinary thing to do, and it only means the
// resulting row has nothing to attach to.
//
// The pane comes from TMUX_PANE, which tmux sets in every pane's environment.
// Asking tmux for "the active pane" instead would be wrong: a hook fires while
// the user may be looking at another pane entirely.
func (r *TmuxResolver) Resolve(ctx context.Context) (session.TmuxTarget, error) {
	// TMUX is set for any process inside a tmux server's pane; without it there is
	// no pane to ask about.
	if r.env("TMUX") == "" {
		return session.TmuxTarget{}, nil
	}
	pane := r.env("TMUX_PANE")
	if pane == "" {
		// Inside tmux but with no pane id is a shell that scrubbed its environment.
		// Worth reporting, because the row will silently not be attachable.
		return session.TmuxTarget{}, fmt.Errorf("reporter: TMUX is set but TMUX_PANE is not, so the pane cannot be identified")
	}

	out, err := r.Runner.Run(ctx, "tmux", "display-message", "-p", "-t", pane, tmuxFormat)
	if err != nil {
		return session.TmuxTarget{}, fmt.Errorf("reporter: asking tmux about pane %s: %w", pane, err)
	}
	// tmux exits 0 with empty output for a pane it does not know — measured on
	// 3.7c with a made-up pane id — so the output is what says whether this
	// worked, not the exit status.
	if out == "" {
		return session.TmuxTarget{}, fmt.Errorf("reporter: tmux does not know pane %s", pane)
	}

	fields := strings.Split(out, "|")
	if len(fields) != 3 {
		return session.TmuxTarget{}, fmt.Errorf("reporter: tmux answered %q, want three |-separated fields", out)
	}
	target := session.TmuxTarget{
		Session: strings.TrimSpace(fields[0]),
		Window:  strings.TrimSpace(fields[1]),
		Pane:    strings.TrimSpace(fields[2]),
	}
	if target.Session == "" {
		return session.TmuxTarget{}, fmt.Errorf("reporter: tmux reported no session name for pane %s", pane)
	}
	return target, nil
}
