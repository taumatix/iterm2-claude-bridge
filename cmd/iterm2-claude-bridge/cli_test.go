package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// runCommand runs the CLI in-process and returns its combined output.
func runCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer

	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// runCommandWithStdin runs the CLI with something on standard input.
func runCommandWithStdin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer

	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestHookRecordsWhatListThenShows(t *testing.T) {
	// The two commands a user runs to check the remote side is working at all.
	dir := t.TempDir()
	payload := `{"session_id":"abc123","hook_event_name":"Notification","cwd":"/srv/app"}`

	_, err := runCommandWithStdin(t, payload, "hook", "--state-dir", dir)
	require.NoError(t, err)

	out, err := runCommand(t, "list", "--state-dir", dir)
	require.NoError(t, err)

	assert.Contains(t, out, "waiting")
	assert.Contains(t, out, "abc123")
	assert.Contains(t, out, "/srv/app")
}

func TestHookAlwaysSucceedsSoItCannotBreakClaude(t *testing.T) {
	// A hook runs on the critical path of every tool call. Failing it would stop
	// Claude from working, which is far worse than a stale row in a panel.
	dir := t.TempDir()

	for _, tc := range []struct{ name, stdin string }{
		{"not json", "this is not json"},
		{"empty", ""},
		{"no session id", `{"hook_event_name":"Stop"}`},
		{"an event we ignore", `{"session_id":"a","hook_event_name":"PostToolBatch"}`},
		{"unknown event", `{"session_id":"a","hook_event_name":"SomethingNew"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCommandWithStdin(t, tc.stdin, "hook", "--state-dir", dir)
			assert.NoError(t, err, "the hook must exit 0 whatever it is given")
		})
	}
}

func TestListSaysSoWhenNothingHasBeenRecorded(t *testing.T) {
	out, err := runCommand(t, "list", "--state-dir", t.TempDir())
	require.NoError(t, err)

	assert.Contains(t, out, "No sessions recorded")
	// The usual cause, worth saying rather than leaving the user to guess.
	assert.Contains(t, out, "PATH")
}

func TestWatchRefusesWithNoHosts(t *testing.T) {
	// It must not try to connect to iTerm2 only to find there is nothing to watch.
	_, err := runCommand(t, "watch")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host")
}

// TestHookAndStreamEndToEnd drives the real binary the way the two halves are
// actually used: hooks append, and a stream over a pipe replays and follows.
//
// This is the whole remote side over its real interfaces — a real process, a real
// pipe, a real file — which is what ssh would be carrying. What it does not cover
// is ssh itself, which needs a reachable host with this program installed.
func TestHookAndStreamEndToEnd(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()

	// One session already recorded before the stream starts, so the replay has
	// something to carry.
	runHook(t, binary, dir, `{"session_id":"before","hook_event_name":"Stop","cwd":"/srv/old"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "stream", "--state-dir", dir, "--poll", "20ms")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	events := make(chan session.Event, 32)
	go func() {
		defer close(events)
		buf := make([]byte, 0, 4096)
		chunk := make([]byte, 1024)
		for {
			n, err := stdout.Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
				for {
					i := bytes.IndexByte(buf, '\n')
					if i < 0 {
						break
					}
					line := buf[:i]
					buf = buf[i+1:]
					if e, decodeErr := session.DecodeEvent(line); decodeErr == nil {
						events <- e
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// The replay.
	first := receive(t, events)
	assert.Equal(t, "before", first.SessionID)
	assert.Equal(t, session.StatusIdle, first.Status)
	assert.Equal(t, "/srv/old", first.Cwd)

	// Then a new hook fires and the stream follows it.
	runHook(t, binary, dir, `{"session_id":"after","hook_event_name":"Notification","cwd":"/srv/new"}`)
	second := receive(t, events)
	assert.Equal(t, "after", second.SessionID)
	assert.Equal(t, session.StatusWaiting, second.Status)

	// And a status change on the same session.
	runHook(t, binary, dir, `{"session_id":"after","hook_event_name":"SessionEnd"}`)
	third := receive(t, events)
	assert.Equal(t, "after", third.SessionID)
	assert.Equal(t, session.StatusGone, third.Status)
}

// TestHookUnderRealTmuxRecordsTheTmuxTarget runs the binary inside a real tmux
// pane, which is the only way to see that the pane it reports is the one it ran
// in.
func TestHookUnderRealTmuxRecordsTheTmuxTarget(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	binary := buildBinary(t)
	dir := t.TempDir()

	name := "iterm2-claude-bridge-cli-test"
	_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	payload := `{"session_id":"in-tmux","hook_event_name":"UserPromptSubmit","cwd":"/srv/app"}`

	// The hook runs inside the pane, so it inherits TMUX and TMUX_PANE exactly as a
	// Claude Code hook would.
	script := "printf %s '" + payload + "' | " + binary + " hook --state-dir " + dir
	start := exec.Command("tmux", "new-session", "-d", "-s", name, "sh", "-c", script+"; sleep 10")
	if out, err := start.CombinedOutput(); err != nil {
		t.Skipf("could not start a tmux session (%v): %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	// The hook is a separate process inside the pane; wait for it to have written.
	deadline := time.Now().Add(10 * time.Second)
	var out string
	for time.Now().Before(deadline) {
		var err error
		out, err = runCommand(t, "list", "--state-dir", dir)
		require.NoError(t, err)
		if strings.Contains(out, "in-tmux") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	require.Contains(t, out, "in-tmux", "the hook did not record anything")
	assert.Contains(t, out, name, "the tmux session it ran in should be recorded, got: %s", out)
	assert.NotContains(t, out, "(not under tmux)")
}

// buildBinary compiles the command once per test that needs it.
func buildBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "iterm2-claude-bridge")
	cmd := exec.Command("go", "build", "-o", path, ".")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building the binary: %s", out)
	return path
}

func runHook(t *testing.T, binary, stateDir, payload string) {
	t.Helper()
	cmd := exec.Command(binary, "hook", "--state-dir", stateDir)
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "running the hook: %s", out)
}

func receive(t *testing.T, events <-chan session.Event) session.Event {
	t.Helper()
	select {
	case e, ok := <-events:
		require.True(t, ok, "the stream ended")
		return e
	case <-time.After(15 * time.Second):
		t.Fatal("no event arrived from the stream")
		return session.Event{}
	}
}

func TestRootCommandListsBothHalves(t *testing.T) {
	out, err := runCommand(t, "--help")
	require.NoError(t, err)

	for _, sub := range []string{"hook", "stream", "watch", "install-hooks", "list"} {
		assert.Contains(t, out, sub)
	}
}

func TestVersionIsReported(t *testing.T) {
	out, err := runCommand(t, "--version")
	require.NoError(t, err)
	assert.Contains(t, out, version)
}
