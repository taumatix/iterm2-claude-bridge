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

// stubSSH writes a script that stands in for ssh as far as quoting is
// concerned: it drops the options, takes the destination, and joins everything
// after it with spaces before handing that to a shell.
//
// That joining is ssh's documented behaviour, not a convenience — ssh(1),
// OpenSSH 10.3: "A complete command line may be specified as command, or it may
// have additional arguments. If supplied, the arguments will be appended to the
// command, separated by spaces, before it is sent to the server to be
// executed." So a `;` that reaches ssh as its own argument reaches the remote
// shell as a bare `;`, whatever quoting put it there.
func stubSSH(t *testing.T, dir string) {
	t.Helper()
	body := `#!/bin/sh
while [ "$1" != "--" ]; do shift; done
shift          # past --
shift          # past the destination
exec sh -c "$*"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(body), 0o700))
}

// runThroughBothShells runs the command string iTerm2 is given, the way iTerm2
// runs it, and returns the arguments tmux ends up with.
//
// There are two shells between this program and tmux, and until v0.1.3 the
// tests only knew about the second one. iTerm2 parses a profile's Command
// before ssh is executed — either with -componentsInShellCommand, or, with
// "Run Command In Login Shell" set, by handing it to the login shell as
// `-i -c <command>`. Either way one level of quoting is consumed there, and
// what survived was tested against the remote shell alone.
func runThroughBothShells(t *testing.T, command, stubDir string) []string {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "running %q: %s", command, out)

	var args []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if after, ok := strings.CutPrefix(line, "ARG:"); ok {
			args = append(args, after)
		}
	}
	return args
}

func TestTmuxGetsOneCommandAfterBothShellsHaveParsedIt(t *testing.T) {
	// The bug this exists for: `tmux select-window -t @1 ';' attach-session …`
	// loses its quotes to the first shell, so ssh is handed `;` as an argument of
	// its own and the remote shell reads it as a command separator. tmux then runs
	// with select-window only, and `attach-session` is looked up as a program —
	// which is exactly the "command not found: attach-session" a user reported.
	stubDir := stubTmux(t)
	stubSSH(t, stubDir)

	command, err := bridge.SSHOptions{}.TabCommand("build-box", session.TmuxTarget{
		Session: "work",
		Window:  "@1",
	})
	require.NoError(t, err)

	got := runThroughBothShells(t, command, stubDir)

	assert.Equal(t, []string{
		"select-window", "-t", "@1", ";", "attach-session", "-t", "work",
	}, got, "the command iTerm2 runs was %q", command)
}

func TestATmuxSessionNameSurvivesBothShells(t *testing.T) {
	stubDir := stubTmux(t)
	stubSSH(t, stubDir)

	// The same names the single-shell test uses, now through both layers. A name
	// safe after one parse is not necessarily safe after two: each parse strips a
	// level of quoting, and the second one runs on a machine this program does
	// not control.
	for _, name := range []string{
		"work",
		"my work",
		"it's",
		"a;echo pwned",
		"$(echo pwned)",
		"`echo pwned`",
		"a|echo pwned",
		"quote\"double",
		"$HOME",
		"paren(s)",
	} {
		t.Run(name, func(t *testing.T) {
			command, err := bridge.SSHOptions{}.TabCommand("box", session.TmuxTarget{Session: name})
			require.NoError(t, err)

			got := runThroughBothShells(t, command, stubDir)

			require.Equal(t, []string{"attach-session", "-t", name}, got,
				"two shells turned %q into %v", command, got)
		})
	}
}

func TestNoInjectedCommandRunsOnThisMachineEither(t *testing.T) {
	// Until v0.1.3 this was a live injection, not a quoting nicety. The command
	// string was assembled by joining arguments with spaces, so the first shell —
	// the one iTerm2 puts between the profile's Command and ssh — expanded
	// whatever the remote host had put in a tmux session name. A session called
	// `$(…)` ran on the Mac, before ssh was even executed. The single-shell test
	// above passed throughout, because it started from the remote half of the
	// command and never saw the first parse.
	stubDir := stubTmux(t)
	stubSSH(t, stubDir)

	marker := filepath.Join(t.TempDir(), "ran-locally")
	command, err := bridge.SSHOptions{}.TabCommand("box", session.TmuxTarget{
		Session: "x$(touch " + marker + ")y",
	})
	require.NoError(t, err)

	got := runThroughBothShells(t, command, stubDir)

	// A file is the evidence, not the output: a name echoed back cannot be told
	// apart from a name executed, and this distinguishes them.
	assert.NoFileExists(t, marker, "the session name was executed on this machine")
	assert.Equal(t, []string{"attach-session", "-t", "x$(touch " + marker + ")y"}, got)
}

func TestTheTmuxTheRemoteReportedIsTheOneInvoked(t *testing.T) {
	// ssh runs the remote command in a non-interactive session, whose PATH often
	// does not carry a tmux installed under /opt/homebrew or /usr/local — the
	// "command not found: tmux" half of the same report. The reporter runs inside
	// the pane, where tmux is on the PATH, so it sends the resolved path and the
	// Mac uses that rather than hoping.
	stubDir := stubTmux(t)
	stubSSH(t, stubDir)

	// Somewhere the PATH does not reach, which is the whole situation being
	// modelled: a tmux the far side can only run if it is named in full.
	elsewhere := filepath.Join(t.TempDir(), "opt", "bin")
	require.NoError(t, os.MkdirAll(elsewhere, 0o700))
	unreachable := filepath.Join(elsewhere, "tmux")
	body := "#!/bin/sh\nfor a in \"$@\"; do printf 'ARG:%s\\n' \"$a\"; done\nprintf 'ARG:from-the-absolute-path\\n'\n"
	require.NoError(t, os.WriteFile(unreachable, []byte(body), 0o700))

	command, err := bridge.SSHOptions{}.TabCommand("box", session.TmuxTarget{
		Session: "work",
		Binary:  unreachable,
	})
	require.NoError(t, err)

	got := runThroughBothShells(t, command, stubDir)
	assert.Equal(t, []string{"attach-session", "-t", "work", "from-the-absolute-path"}, got,
		"the tmux the reporter named should be the one that ran, not the one on PATH")

	// And with no path reported — an older remote build — the bare name still
	// finds whatever tmux the PATH has.
	command, err = bridge.SSHOptions{}.TabCommand("box", session.TmuxTarget{Session: "work"})
	require.NoError(t, err)
	assert.Equal(t, []string{"attach-session", "-t", "work"}, runThroughBothShells(t, command, stubDir))
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
