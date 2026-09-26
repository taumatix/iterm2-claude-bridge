package bridge_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/bridge"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// stubTmux writes a script that reports each argument it received on its own
// line, prefixed, so a test can see exactly how a real shell split the command.
//
// The remote command is a string that a shell on the far side parses. Asserting
// the string this program builds only proves what was written; running it through
// a real shell proves what tmux would actually be handed, which is the thing that
// matters when the session name came from another machine.
func stubTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "tmux")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf 'ARG:%s\\n' \"$a\"; done\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o700))
	return dir
}

// runRemote runs the remote half of an attach command through a real shell with
// the stub tmux ahead of the real one on PATH.
func runRemote(t *testing.T, remote, stubDir string) []string {
	t.Helper()
	cmd := exec.Command("sh", "-c", remote)
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "running %q: %s", remote, out)

	var args []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if after, ok := strings.CutPrefix(line, "ARG:"); ok {
			args = append(args, after)
		}
	}
	return args
}

func TestQuotingSurvivesARealShell(t *testing.T) {
	stubDir := stubTmux(t)

	// Every one of these is a plausible tmux session name and a shell metacharacter
	// problem. The test asserts tmux receives the name as exactly one argument,
	// byte for byte.
	for _, name := range []string{
		"work",
		"my work",
		"it's",
		"a;echo pwned",
		"$(echo pwned)",
		"`echo pwned`",
		"a|echo pwned",
		"a&&echo pwned",
		"glob*",
		"quote\"double",
		"back\\slash",
		"tab\there",
		"$HOME",
		"a}brace{b",
		"~tilde",
		"!bang",
		"paren(s)",
	} {
		t.Run(name, func(t *testing.T) {
			_, args, err := bridge.SSHOptions{}.AttachCommand("box", session.TmuxTarget{Session: name})
			require.NoError(t, err)
			remote := args[len(args)-1]

			got := runRemote(t, remote, stubDir)

			// tmux must see exactly: attach-session, -t, <the name>.
			require.Equal(t, []string{"attach-session", "-t", name}, got,
				"a real shell split %q into %v", remote, got)
		})
	}
}

func TestQuotingSurvivesARealShellWithAWindowToo(t *testing.T) {
	stubDir := stubTmux(t)

	_, args, err := bridge.SSHOptions{}.AttachCommand("box", session.TmuxTarget{
		Session: "a;echo pwned",
		Window:  "@3",
	})
	require.NoError(t, err)

	got := runRemote(t, args[len(args)-1], stubDir)

	// One tmux invocation receives both commands, separated by a literal ";" that
	// the shell must hand through rather than act on.
	assert.Equal(t, []string{
		"select-window", "-t", "@3", ";", "attach-session", "-t", "a;echo pwned",
	}, got)
}

func TestNoInjectedCommandEverRuns(t *testing.T) {
	// The sharpest form of the same check: if quoting failed, the shell would run
	// the injected command and its output would appear.
	stubDir := stubTmux(t)

	_, args, err := bridge.SSHOptions{}.AttachCommand("box",
		session.TmuxTarget{Session: "x; echo INJECTED; :"})
	require.NoError(t, err)

	cmd := exec.Command("sh", "-c", args[len(args)-1])
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)

	// The stub echoes each argument prefixed with "ARG:", and the name it received
	// contains the word too — so "does the output mention INJECTED" cannot tell the
	// two apart. A bare line is what `echo INJECTED` produces, and only the shell
	// running the name could produce that.
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		assert.NotEqual(t, "INJECTED", line,
			"the session name was executed rather than passed as an argument: %q", out)
	}
	assert.Contains(t, string(out), "ARG:x; echo INJECTED; :",
		"the whole name should arrive as one argument")
}
