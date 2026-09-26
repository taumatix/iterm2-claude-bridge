package bridge_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/bridge"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

func TestStreamCommandAsksForNoTTYAndFailsFast(t *testing.T) {
	program, args := bridge.SSHOptions{}.StreamCommand("build-box", "")
	line := strings.Join(args, " ")

	assert.Equal(t, "ssh", program)
	// A TTY would turn on echo and line-ending translation on a pipe carrying JSON.
	assert.NotContains(t, args, "-t")
	// Sitting at a password prompt nobody can see would look like a quiet host.
	assert.Contains(t, line, "BatchMode=yes")
	// A dropped connection should be noticed in about a minute.
	assert.Contains(t, line, "ServerAliveInterval=30")
	assert.Contains(t, line, "build-box")
	assert.Contains(t, line, bridge.DefaultRemoteCommand)
}

func TestStreamCommandUsesTheRemoteCommandGiven(t *testing.T) {
	_, args := bridge.SSHOptions{}.StreamCommand("box", "/opt/bin/bridge stream --verbose")
	assert.Contains(t, strings.Join(args, " "), "/opt/bin/bridge stream --verbose")
}

func TestStreamCommandPassesExtraArgsBeforeTheDestination(t *testing.T) {
	// The user's ssh config is where login name, port and identity belong; these
	// are for what it cannot express.
	opts := bridge.SSHOptions{Args: []string{"-J", "jump-host"}}
	_, args := opts.StreamCommand("box", "")

	jump := indexOf(args, "-J")
	host := indexOf(args, "box")
	require.GreaterOrEqual(t, jump, 0)
	require.GreaterOrEqual(t, host, 0)
	assert.Less(t, jump, host, "options must come before the destination")
}

func TestAttachCommandRequestsATTYAndAttachesToTheSession(t *testing.T) {
	program, args, err := bridge.SSHOptions{}.AttachCommand("build-box",
		session.TmuxTarget{Session: "work", Window: "@3", Pane: "%7"})
	require.NoError(t, err)

	assert.Equal(t, "ssh", program)
	// tmux refuses to attach without a TTY.
	assert.Contains(t, args, "-t")
	assert.Contains(t, args, "build-box")

	remote := args[len(args)-1]
	// tmux takes a lone ";" as a command separator, verified against tmux 3.7c, and
	// it is quoted so the shell hands it through instead of acting on it.
	// quoting_test.go proves that against a real shell; this only pins the string.
	assert.Equal(t, `tmux select-window -t @3 ';' attach-session -t work`, remote)
}

func TestAttachCommandOmitsSelectWindowWhenNoWindowIsKnown(t *testing.T) {
	_, args, err := bridge.SSHOptions{}.AttachCommand("box", session.TmuxTarget{Session: "work"})
	require.NoError(t, err)

	remote := args[len(args)-1]
	assert.Equal(t, "tmux attach-session -t work", remote)
	assert.NotContains(t, remote, "select-window")
}

func TestAttachCommandRefusesWithNoTmuxSession(t *testing.T) {
	_, _, err := bridge.SSHOptions{}.AttachCommand("box", session.TmuxTarget{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "box")
}

func TestAttachCommandQuotesNamesThatWouldOtherwiseBecomeShellSyntax(t *testing.T) {
	// The remote command reaches a shell on the far side. A tmux session name is
	// chosen by whoever made the session, so it is not this program's to trust.
	cases := map[string]string{
		"my work":         `'my work'`,
		"a;rm -rf /":      `'a;rm -rf /'`,
		"back`tick`":      "'back`tick`'",
		"$(whoami)":       `'$(whoami)'`,
		"it's":            `'it'\''s'`,
		"plain-name_1":    `plain-name_1`,
		"pipe|to|nowhere": `'pipe|to|nowhere'`,
		"amp&background":  `'amp&background'`,
		"redirect>file":   `'redirect>file'`,
		"glob*star":       `'glob*star'`,
		"newline\ninject": "'newline\ninject'",
	}
	for name, wantQuoted := range cases {
		t.Run(name, func(t *testing.T) {
			_, args, err := bridge.SSHOptions{}.AttachCommand("box", session.TmuxTarget{Session: name})
			require.NoError(t, err)

			remote := args[len(args)-1]
			assert.Equal(t, "tmux attach-session -t "+wantQuoted, remote)
		})
	}
}

func TestAttachCommandQuotesAnEmptyWindowSafely(t *testing.T) {
	// An empty window is skipped rather than quoted as '', which tmux would reject.
	_, args, err := bridge.SSHOptions{}.AttachCommand("box",
		session.TmuxTarget{Session: "work", Window: ""})
	require.NoError(t, err)
	assert.NotContains(t, args[len(args)-1], "''")
}

func TestSSHOptionsHonourAnAlternativeProgram(t *testing.T) {
	program, _ := bridge.SSHOptions{Program: "/usr/local/bin/ssh"}.StreamCommand("box", "")
	assert.Equal(t, "/usr/local/bin/ssh", program)
}

func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}
