package reporter_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/reporter"
)

// fakeRunner stands in for tmux, recording what it was asked and answering from
// a script.
type fakeRunner struct {
	stdout string
	err    error
	calls  [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.stdout, f.err
}

// envFrom builds a lookup over a fixed map, so a test does not have to mutate
// the process environment.
func envFrom(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestResolveReportsNoTargetOutsideTmux(t *testing.T) {
	// Running Claude outside tmux is ordinary. It only means the row has nothing
	// to attach to, so it must not be an error.
	runner := &fakeRunner{}
	r := &reporter.TmuxResolver{Runner: runner, Env: envFrom(nil)}

	target, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.True(t, target.Zero())
	assert.Empty(t, runner.calls, "tmux should not be run when TMUX is unset")
}

func TestResolveAsksAboutTheHooksOwnPane(t *testing.T) {
	// A hook fires while the user may be looking at a different pane, so asking
	// tmux for "the active pane" would attribute the session to the wrong place.
	runner := &fakeRunner{stdout: "build|@3|%7"}
	r := &reporter.TmuxResolver{
		Runner: runner,
		Env:    envFrom(map[string]string{"TMUX": "/tmp/tmux-501/default,123,0", "TMUX_PANE": "%7"}),
	}

	target, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "build", target.Session)
	assert.Equal(t, "@3", target.Window)
	assert.Equal(t, "%7", target.Pane)

	require.Len(t, runner.calls, 1)
	call := strings.Join(runner.calls[0], " ")
	assert.Contains(t, call, "-t %7", "the pane from TMUX_PANE must be the target")
	assert.Contains(t, call, "display-message")
}

func TestResolveRecordsWhereTmuxIs(t *testing.T) {
	// The far side attaches over ssh, in a non-interactive session whose PATH
	// often has no /opt/homebrew/bin — so it cannot find tmux and says so. Here,
	// inside the pane, tmux is on the PATH by definition.
	runner := &fakeRunner{stdout: "build|@3|%7"}
	r := &reporter.TmuxResolver{
		Runner:   runner,
		Env:      envFrom(map[string]string{"TMUX": "set", "TMUX_PANE": "%7"}),
		LookPath: func(string) (string, error) { return "/opt/homebrew/bin/tmux", nil },
	}

	target, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/opt/homebrew/bin/tmux", target.Binary)
}

func TestResolveStillReportsAPaneWhenTmuxCannotBeLocated(t *testing.T) {
	// The pane was resolved, so the row is attachable. The far side falls back to
	// the bare name, which is all it ever had — losing the row instead would be a
	// worse answer than the one that worked before.
	runner := &fakeRunner{stdout: "build|@3|%7"}
	r := &reporter.TmuxResolver{
		Runner:   runner,
		Env:      envFrom(map[string]string{"TMUX": "set", "TMUX_PANE": "%7"}),
		LookPath: func(string) (string, error) { return "", errors.New("not on PATH") },
	}

	target, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Empty(t, target.Binary)
	assert.Equal(t, "build", target.Session)
	assert.False(t, target.Zero())
}

func TestResolveFailsWhenTmuxDoesNotKnowThePane(t *testing.T) {
	// Measured on tmux 3.7c: display-message exits 0 with empty output for a pane
	// it does not know, so the output is what says whether this worked.
	runner := &fakeRunner{stdout: ""}
	r := &reporter.TmuxResolver{
		Runner: runner,
		Env:    envFrom(map[string]string{"TMUX": "x", "TMUX_PANE": "%9999"}),
	}

	_, err := r.Resolve(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "%9999")
}

func TestResolveFailsOnAnAnswerItCannotRead(t *testing.T) {
	runner := &fakeRunner{stdout: "build|@3"}
	r := &reporter.TmuxResolver{
		Runner: runner,
		Env:    envFrom(map[string]string{"TMUX": "x", "TMUX_PANE": "%7"}),
	}

	_, err := r.Resolve(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "three")
}

func TestResolveFailsWhenInsideTmuxWithNoPaneID(t *testing.T) {
	// A shell that scrubbed its environment. Worth saying, because the row will
	// silently not be attachable.
	r := &reporter.TmuxResolver{
		Runner: &fakeRunner{},
		Env:    envFrom(map[string]string{"TMUX": "x"}),
	}

	_, err := r.Resolve(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TMUX_PANE")
}

func TestResolveSurfacesATmuxFailure(t *testing.T) {
	runner := &fakeRunner{err: errors.New("no server running")}
	r := &reporter.TmuxResolver{
		Runner: runner,
		Env:    envFrom(map[string]string{"TMUX": "x", "TMUX_PANE": "%1"}),
	}

	_, err := r.Resolve(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no server running")
}

// TestResolveAgainstRealTmux drives the actual tmux binary in a real session.
//
// The fake above proves the parsing; only this proves the format string and the
// -t flag are what tmux accepts, which is the part that would silently produce
// an unattachable row if it were wrong.
func TestResolveAgainstRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}

	name := "iterm2-claude-bridge-test-resolve"
	// A detached session with a shell that just waits, so the pane stays alive.
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "sleep 300")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not start a tmux session (%v): %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	})

	paneOut, err := exec.Command("tmux", "list-panes", "-t", name, "-F", "#{pane_id}").Output()
	require.NoError(t, err)
	pane := strings.TrimSpace(string(paneOut))
	require.NotEmpty(t, pane)

	// TMUX and TMUX_PANE are what tmux sets inside a pane; this is the environment
	// a hook process would inherit.
	r := &reporter.TmuxResolver{
		Runner: reporter.ExecRunner{},
		Env:    envFrom(map[string]string{"TMUX": "set", "TMUX_PANE": pane}),
	}

	target, err := r.Resolve(context.Background())
	require.NoError(t, err)
	assert.Equal(t, name, target.Session)
	assert.Equal(t, pane, target.Pane)
	// tmux reports ids with their sigils, which is what it wants back as a target.
	assert.True(t, strings.HasPrefix(target.Window, "@"), "window id %q should start with @", target.Window)
	assert.True(t, strings.HasPrefix(target.Pane, "%"), "pane id %q should start with %%", target.Pane)
	assert.False(t, target.Zero())

	// The path the far side will run over ssh, resolved here against the real
	// PATH — and it must be the tmux that actually owns this pane, since tmux
	// refuses a client whose protocol version differs from its server's.
	wanted, err := exec.LookPath("tmux")
	require.NoError(t, err)
	assert.Equal(t, wanted, target.Binary)
	assert.True(t, filepath.IsAbs(target.Binary), "%q should be absolute", target.Binary)
}

func TestExecRunnerReportsStderrFromAFailedCommand(t *testing.T) {
	// "exit status 1" says nothing about what went wrong; tmux's own message does.
	// Driven with sh rather than tmux because tmux's wording depends on whether a
	// server happens to be running, which is not what this is checking.
	_, err := reporter.ExecRunner{}.Run(context.Background(), "sh", "-c", "echo 'no server running' >&2; exit 1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no server running")
}

func TestExecRunnerReturnsTrimmedOutput(t *testing.T) {
	out, err := reporter.ExecRunner{}.Run(context.Background(), "sh", "-c", "echo '  spaced  '")
	require.NoError(t, err)
	assert.Equal(t, "spaced", out)
}
