package bridge_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/bridge"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// quietLog keeps expected warnings out of the test output.
func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// scriptLauncher runs a real subprocess as the transport.
//
// This is what ssh is from the watcher's point of view: a command whose standard
// output is the event stream. Driving a real process rather than an in-memory
// reader exercises the pipe, the line buffering and the process teardown — the
// parts that break in practice. What it does not exercise is ssh itself, which
// needs a reachable host.
type scriptLauncher struct {
	script string

	mu     sync.Mutex
	starts int
}

func (l *scriptLauncher) Start(ctx context.Context, host string) (io.ReadCloser, func() error, error) {
	l.mu.Lock()
	l.starts++
	l.mu.Unlock()

	cmd := exec.CommandContext(ctx, "sh", "-c", l.script)
	cmd.Env = append(os.Environ(), "BRIDGE_TEST_HOST="+host)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return stdout, cmd.Wait, nil
}

func (l *scriptLauncher) startCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts
}

// eventLine renders one event as the remote would write it, newline included.
func eventLine(t *testing.T, id string, status session.Status, tmuxSession string) string {
	t.Helper()
	e := session.Event{
		SessionID: id,
		Status:    status,
		At:        time.Now().UTC(),
		Cwd:       "/srv/app",
	}
	if tmuxSession != "" {
		e.Tmux = session.TmuxTarget{Session: tmuxSession, Window: "@1"}
	}
	line, err := e.Encode()
	require.NoError(t, err)
	return string(line)
}

// emit returns a shell fragment that writes lines verbatim.
//
// The lines go through a file rather than through printf. Interpolating JSON into
// a shell command means quoting it twice, and the first version of this helper got
// it wrong in a way that produced a literal backslash-n instead of a newline — so
// nothing was ever a complete line and five tests failed for a reason that had
// nothing to do with the watcher.
func emit(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600))
	return "cat " + path
}

// holdOpen keeps a stream from ending, as a real stream would not end.
//
// Short, because the shell's child inherits the pipe: a long sleep outliving the
// test would hold the write end open after the test had finished with it.
const holdOpen = "; sleep 5"

// watchInBackground starts a watch and stops it on cleanup.
func watchInBackground(t *testing.T, w *bridge.Watcher, hosts ...string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Watch(ctx, hosts)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the watch did not stop when cancelled")
		}
	})
}

// waitFor polls until condition holds, failing the test otherwise.
func waitFor(t *testing.T, why string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func TestWatchReadsEventsFromARealSubprocess(t *testing.T) {
	registry := session.NewRegistry()
	line := eventLine(t, "s1", session.StatusWaiting, "work")
	// sleep keeps the stream open, as a real stream would be.
	launcher := &scriptLauncher{script: emit(t, line) + holdOpen}

	w := &bridge.Watcher{Registry: registry, Launcher: launcher, Log: quietLog()}
	watchInBackground(t, w, "build-box")

	waitFor(t, "the event to arrive", func() bool { return len(registry.Sessions()) == 1 })

	got := registry.Sessions()[0]
	assert.Equal(t, "s1", got.SessionID)
	assert.Equal(t, session.StatusWaiting, got.Status)
	// The remote does not know what the local side calls it, and that name is what
	// goes back into an ssh command.
	assert.Equal(t, "build-box", got.Host)
	assert.True(t, got.Attachable())
}

func TestWatchLabelsEachHostsEventsWithThatHost(t *testing.T) {
	registry := session.NewRegistry()
	// Both hosts report the same Claude session id, which is what happens when the
	// id is only unique per machine.
	line := eventLine(t, "same-id", session.StatusWorking, "work")
	launcher := &scriptLauncher{script: emit(t, line) + holdOpen}

	w := &bridge.Watcher{Registry: registry, Launcher: launcher, Log: quietLog()}
	watchInBackground(t, w, "box-a", "box-b")

	waitFor(t, "both hosts to report", func() bool { return len(registry.Sessions()) == 2 })

	hosts := map[string]bool{}
	for _, s := range registry.Sessions() {
		hosts[s.Host] = true
	}
	assert.Equal(t, map[string]bool{"box-a": true, "box-b": true}, hosts)
}

func TestWatchSkipsHeartbeatsAndUnreadableLines(t *testing.T) {
	registry := session.NewRegistry()
	good := eventLine(t, "s1", session.StatusIdle, "work")
	// A blank heartbeat, a corrupt line, then a real event.
	script := emit(t, "\n", "{not json}\n", good) + holdOpen
	launcher := &scriptLauncher{script: script}

	w := &bridge.Watcher{Registry: registry, Launcher: launcher, Log: quietLog()}
	watchInBackground(t, w, "box")

	waitFor(t, "the good event after the bad ones", func() bool { return len(registry.Sessions()) == 1 })
	assert.Equal(t, "s1", registry.Sessions()[0].SessionID)
}

func TestWatchForgetsAHostsSessionsWhenItsStreamEnds(t *testing.T) {
	// Showing the last thing heard would be a lie that looks exactly like the
	// truth: those sessions may still be running, but nothing here knows.
	registry := session.NewRegistry()
	line := eventLine(t, "s1", session.StatusWorking, "work")

	// The stream stays open until the test says so. Letting it end on its own made
	// this race: the host was forgotten before the test could observe the event
	// having arrived, so the first wait timed out about as often as it passed.
	stop := filepath.Join(t.TempDir(), "stop")
	script := emit(t, line) + "; while [ ! -f " + stop + " ]; do sleep 0.02; done"
	launcher := &scriptLauncher{script: script}

	w := &bridge.Watcher{
		Registry: registry,
		Launcher: launcher,
		// A long first retry, so the test observes the gap rather than a reconnect.
		Backoff: bridge.Backoff{Min: 10 * time.Second, Max: time.Minute},
		Log:     quietLog(),
	}
	watchInBackground(t, w, "box")

	waitFor(t, "the event to arrive", func() bool { return len(registry.Sessions()) == 1 })

	require.NoError(t, os.WriteFile(stop, nil, 0o600))
	waitFor(t, "the sessions to be forgotten once the stream ended", func() bool {
		return len(registry.Sessions()) == 0
	})
}

func TestWatchReconnectsAfterTheStreamEnds(t *testing.T) {
	registry := session.NewRegistry()
	line := eventLine(t, "s1", session.StatusWorking, "work")
	launcher := &scriptLauncher{script: emit(t, line)}

	w := &bridge.Watcher{
		Registry: registry,
		Launcher: launcher,
		Backoff:  bridge.Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond},
		Log:      quietLog(),
	}
	watchInBackground(t, w, "box")

	waitFor(t, "several reconnections", func() bool { return launcher.startCount() >= 3 })
}

func TestWatchKeepsTryingAHostThatCannotBeReached(t *testing.T) {
	// One unreachable host must not stop the others, and must not spin.
	registry := session.NewRegistry()
	launcher := &scriptLauncher{script: "exit 255"} // ssh's "could not connect".

	w := &bridge.Watcher{
		Registry: registry,
		Launcher: launcher,
		Backoff:  bridge.Backoff{Min: 10 * time.Millisecond, Max: 30 * time.Millisecond},
		Log:      quietLog(),
	}
	watchInBackground(t, w, "unreachable")

	waitFor(t, "retries against the unreachable host", func() bool { return launcher.startCount() >= 3 })
	assert.Empty(t, registry.Sessions())
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	b := bridge.Backoff{Min: time.Second, Max: 8 * time.Second}
	assert.Equal(t, time.Second, b.Next(0), "the first retry waits Min")
	assert.Equal(t, 2*time.Second, b.Next(1))
	assert.Equal(t, 4*time.Second, b.Next(2))
	assert.Equal(t, 8*time.Second, b.Next(3), "capped")
	assert.Equal(t, 8*time.Second, b.Next(50), "still capped after many failures")
}

func TestBackoffFallsBackToTheDefaultWhenUnset(t *testing.T) {
	assert.Positive(t, bridge.Backoff{}.Next(0))
	assert.LessOrEqual(t, bridge.Backoff{}.Next(50), bridge.DefaultBackoff.Max)
}

func TestWatchRemovesAnEndedSessionAfterTheGrace(t *testing.T) {
	// A human should get a moment to see that a session finished.
	registry := session.NewRegistry()
	working := eventLine(t, "s1", session.StatusWorking, "work")
	gone := eventLine(t, "s1", session.StatusGone, "work")
	script := emit(t, working) + "; sleep 0.1; " + emit(t, gone) + holdOpen

	w := &bridge.Watcher{
		Registry:        registry,
		Launcher:        &scriptLauncher{script: script},
		ForgetGoneAfter: 100 * time.Millisecond,
		Log:             quietLog(),
	}
	watchInBackground(t, w, "box")

	waitFor(t, "the session to be reported gone", func() bool {
		s, ok := registry.Lookup(session.Key("box", "s1"))
		return ok && s.Status == session.StatusGone
	})
	waitFor(t, "the ended session's row to disappear", func() bool {
		_, ok := registry.Lookup(session.Key("box", "s1"))
		return !ok
	})
}

func TestSSHLauncherForwardsRemoteStderrToTheLog(t *testing.T) {
	// Without this, ssh's "host unreachable" vanishes and an unreachable host looks
	// like a quiet one.
	var seen atomic.Bool
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		if strings.Contains(string(p), "no route to host") {
			seen.Store(true)
		}
		return len(p), nil
	}), nil))

	// A stub "ssh" that writes to stderr and exits, so no network is needed.
	dir := t.TempDir()
	stub := filepath.Join(dir, "ssh")
	require.NoError(t, os.WriteFile(stub,
		[]byte("#!/bin/sh\necho 'no route to host' >&2\nexit 255\n"), 0o700))

	launcher := bridge.SSHLauncher{SSH: bridge.SSHOptions{Program: stub}, Log: log}
	stdout, wait, err := launcher.Start(context.Background(), "box")
	require.NoError(t, err)

	_, _ = io.ReadAll(stdout)
	_ = wait()

	// Asserted outright rather than waited for: wait() returning is the guarantee
	// that stderr has been drained, so polling here would hide the bug this test
	// exists for instead of failing on it.
	//
	// It used to poll, and it passed on the machine that wrote it and on macOS CI
	// while failing on Linux — because cmd.Wait closes the stderr pipe the moment
	// the process exits, and whether the drain won that race was up to the
	// scheduler. A 5-second poll cannot fix a line that was thrown away.
	assert.True(t, seen.Load(), "ssh wrote to stderr and it never reached the log")
}

// writerFunc adapts a function to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
