// Package bridge is the half that runs on the Mac with iTerm2.
//
// It streams status from each watched host, keeps a registry of what is running
// where, shows it in a toolbelt panel, and opens a tab that SSHes in and
// attaches to the right tmux session when a row is clicked.
package bridge

import (
	"fmt"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
	"github.com/taumatix/iterm2-claude-bridge/shared/shellquote"
)

// DefaultRemoteCommand is the reporter invocation the local half runs over SSH.
// It is a bare name so the remote PATH resolves it, which is what lets the
// remote side be upgraded independently.
const DefaultRemoteCommand = "iterm2-claude-bridge stream"

// SSHOptions shape the ssh command lines this package builds.
type SSHOptions struct {
	// Program is the ssh binary. Defaults to "ssh".
	Program string

	// Args are extra arguments placed before the destination, for options the
	// user's ssh config cannot express.
	//
	// The user is expected to configure hosts in ~/.ssh/config: that is where the
	// login name, port, jump host and identity belong, and duplicating them here
	// would mean two places to change.
	Args []string
}

func (o SSHOptions) program() string {
	if o.Program != "" {
		return o.Program
	}
	return "ssh"
}

// StreamCommand builds the command that follows a host's status.
//
// It asks for no TTY: this is a pipe carrying newline-delimited JSON, and a TTY
// would turn on line-ending translation and echo.
func (o SSHOptions) StreamCommand(host, remoteCommand string) (string, []string) {
	if remoteCommand == "" {
		remoteCommand = DefaultRemoteCommand
	}
	args := append([]string{}, o.Args...)
	args = append(args,
		// Fail rather than sit at a password prompt no one can see; the panel
		// reports the host as unreachable instead.
		"-o", "BatchMode=yes",
		// A dropped connection should be noticed in about a minute, not held open
		// by the kernel for hours.
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"--", host, remoteCommand,
	)
	return o.program(), args
}

// AttachCommand builds the command that attaches to a session's tmux target.
//
// It asks for a TTY with -t, because tmux refuses to attach without one. The
// remote side is one shell word per argument, quoted here, so a tmux session
// name containing a space or a quote cannot turn into extra shell words.
func (o SSHOptions) AttachCommand(host string, target session.TmuxTarget) (string, []string, error) {
	if target.Session == "" {
		return "", nil, fmt.Errorf("bridge: no tmux session to attach to on %s", host)
	}

	// tmux takes `;` as a command separator when it arrives as its own argument,
	// verified against tmux 3.7c. Selecting the window before attaching puts the
	// user on the pane Claude is in rather than wherever the session was last
	// left.
	//
	// The separator is quoted because this string is parsed by a shell first: bare,
	// the shell consumes it and tries to run `attach-session` as its own command,
	// which selects the window and then fails with "command not found". Asserting
	// the built string cannot catch that — only running it through a real shell can,
	// which is what quoting_test.go does.
	remote := []string{"tmux"}
	if target.Window != "" {
		remote = append(remote, "select-window", "-t", target.Window, ";")
	}
	remote = append(remote, "attach-session", "-t", target.Session)

	args := append([]string{}, o.Args...)
	args = append(args,
		"-t", // tmux will not attach without a TTY.
		"--", host,
		shellquote.Words(remote...),
	)
	return o.program(), args, nil
}
