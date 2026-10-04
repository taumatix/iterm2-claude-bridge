package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// A session whose Claude dies without a SessionEnd hook (killed, crashed, the
// machine's OOM killer) used to keep its last status for ever: a waiting row
// still said "needs you". The hook now records which process it ran for, and
// the stream reports the session gone once that process has exited.
//
// Claude runs a hook through `sh -c`, so the hook's parent is a shell and the
// process it reports for is the first ancestor that is not one. Here perl
// plays Claude: system() with a redirection runs the hook through /bin/sh,
// exactly as Claude does, and perl stays alive until it is killed.
func TestTheStreamReportsASessionGoneWhenItsProcessExits(t *testing.T) {
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl plays Claude in this test")
	}
	binary := buildBinary(t)
	dir := t.TempDir()
	payload := filepath.Join(dir, "payload.json")
	require.NoError(t, os.WriteFile(payload,
		[]byte(`{"session_id":"killed","hook_event_name":"Notification","cwd":"/srv/app"}`), 0o600))

	claude := exec.Command("perl", "-e",
		fmt.Sprintf(`system(q{%s hook --state-dir %s < %s}) == 0 or die; sleep 300`, binary, dir, payload))
	require.NoError(t, claude.Start())
	t.Cleanup(func() { _ = claude.Process.Kill(); _ = claude.Wait() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The hook has run once its event is in the log.
	require.Eventually(t, func() bool {
		out, err := exec.Command(binary, "list", "--state-dir", dir).CombinedOutput()
		return err == nil && strings.Contains(string(out), "killed")
	}, 10*time.Second, 50*time.Millisecond, "the hook never recorded the session")

	stream := exec.CommandContext(ctx, binary, "stream", "--state-dir", dir,
		"--poll", "20ms", "--check-processes", "100ms")
	stdout, err := stream.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, stream.Start())
	t.Cleanup(func() { cancel(); _ = stream.Wait() })
	events := make(chan session.Event, 32)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if e, err := session.DecodeEvent(sc.Bytes()); err == nil {
				events <- e
			}
		}
	}()

	first := receive(t, events)
	require.Equal(t, "killed", first.SessionID)
	require.Equal(t, session.StatusWaiting, first.Status)
	require.NotNil(t, first.Process, "the hook did not record the process it ran for")
	assert.Equal(t, claude.Process.Pid, first.Process.PID,
		"the hook recorded its shell, or itself, instead of the process that ran it")

	// Alive: nothing more for a while.
	select {
	case e := <-events:
		t.Fatalf("the stream reported %s for a session whose process is still running", e.Status)
	case <-time.After(500 * time.Millisecond):
	}

	require.NoError(t, claude.Process.Kill())
	_ = claude.Wait()

	gone := receive(t, events)
	assert.Equal(t, "killed", gone.SessionID)
	assert.Equal(t, session.StatusGone, gone.Status)
	assert.Equal(t, "/srv/app", gone.Cwd, "the gone event should still say where the session was")
}
