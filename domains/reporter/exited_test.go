package reporter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// A session resumed with `claude --resume` keeps its id under a new process.
// When the stream finds the old process dead just as the new one's first hook
// lands in the log, the hook is newer than anything the stream has followed.
// Appending "gone" then would end a session that is running again.
func TestAnExitIsNotReportedForASessionAResumedProcessHasTakenOver(t *testing.T) {
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	old := &session.Process{PID: 100, Started: "old"}
	resumed := &session.Process{PID: 200, Started: "new"}

	first := session.Event{SessionID: "s", Status: session.StatusWaiting, At: time.Now().UTC().Add(-time.Minute), Process: old}
	require.NoError(t, store.Append(first))
	s := &Stream{
		Store:  store,
		Alive:  func(_ context.Context, p session.Process) (bool, error) { return p.PID == resumed.PID, nil },
		latest: map[string]session.Event{"s": first},
	}

	// The resumed process's hook, in the log but not yet followed.
	require.NoError(t, store.Append(session.Event{SessionID: "s", Status: session.StatusWorking, Process: resumed}))

	require.NoError(t, s.reportExited(context.Background()))

	events, err := store.Replay()
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, session.StatusWorking, events[0].Status, "the resumed session was reported gone")
	assert.Equal(t, 200, events[0].Process.PID)
}

// The control: with nothing newer in the log, a dead process still ends the
// session.
func TestAnExitIsReportedWhenNothingNewerHasArrived(t *testing.T) {
	store, err := NewStore(t.TempDir())
	require.NoError(t, err)
	old := &session.Process{PID: 100, Started: "old"}
	first := session.Event{SessionID: "s", Status: session.StatusWaiting, At: time.Now().UTC().Add(-time.Minute), Process: old}
	require.NoError(t, store.Append(first))
	s := &Stream{
		Store:  store,
		Alive:  func(context.Context, session.Process) (bool, error) { return false, nil },
		latest: map[string]session.Event{"s": first},
	}

	require.NoError(t, s.reportExited(context.Background()))

	events, err := store.Replay()
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, session.StatusGone, events[0].Status)
	assert.Equal(t, ProcessExited, events[0].HookEvent)
}
