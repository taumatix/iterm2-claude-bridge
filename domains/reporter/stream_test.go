package reporter_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/reporter"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// syncBuffer collects stream output while the stream goroutine writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// events parses everything written so far, skipping heartbeats.
func (b *syncBuffer) events(t *testing.T) []session.Event {
	t.Helper()
	var out []session.Event
	for _, line := range strings.Split(b.String(), "\n") {
		e, err := session.DecodeEvent([]byte(line))
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

// runStream starts a stream in the background and stops it on cleanup.
func runStream(t *testing.T, s *reporter.Stream) *syncBuffer {
	t.Helper()
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			// Cancellation is not a failure.
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("the stream did not stop when cancelled")
		}
	})
	return out
}

// waitForEvents polls until the stream has written n events.
func waitForEvents(t *testing.T, out *syncBuffer, n int) []session.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := out.events(t); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := out.events(t)
	t.Fatalf("wanted %d events, got %d: %q", n, len(got), out.String())
	return nil
}

func TestStreamReplaysCurrentStateBeforeFollowing(t *testing.T) {
	// A watcher that has just connected must see every session immediately, not
	// only those that change from now on.
	store := newStore(t)
	require.NoError(t, store.Append(ev("s1", session.StatusWaiting, 0)))
	require.NoError(t, store.Append(ev("s2", session.StatusWorking, 1)))

	out := runStream(t, &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond})

	got := waitForEvents(t, out, 2)
	require.Len(t, got, 2)
	assert.Equal(t, "s1", got[0].SessionID)
	assert.Equal(t, "s2", got[1].SessionID)
}

func TestStreamFollowsEventsAppendedAfterItStarted(t *testing.T) {
	store := newStore(t)
	out := runStream(t, &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond})

	require.NoError(t, store.Append(ev("s1", session.StatusWorking, 0)))
	got := waitForEvents(t, out, 1)
	assert.Equal(t, session.StatusWorking, got[0].Status)

	require.NoError(t, store.Append(ev("s1", session.StatusIdle, 1)))
	got = waitForEvents(t, out, 2)
	assert.Equal(t, session.StatusIdle, got[1].Status)
}

func TestStreamDoesNotReSendWhatItAlreadyReplayed(t *testing.T) {
	// Following starts at the end of the file as it was when the replay was taken.
	store := newStore(t)
	require.NoError(t, store.Append(ev("s1", session.StatusWorking, 0)))

	out := runStream(t, &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond})
	waitForEvents(t, out, 1)

	// Give it several poll cycles to get it wrong.
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, out.events(t), 1, "the replayed event should not be sent again")
}

// TestStreamDoesNotLoseAnEventAppendedDuringTheReplay pins the bug that cost this
// change a red CI: a hook firing while the replay is being read was past the
// replay's view of the file and behind the offset taken after it finished, so
// neither step reported it and nothing said so.
//
// Every watcher connecting to a busy host hit that window. It surfaced as one
// macOS job failing a suite that was green on the machine that wrote it, and as a
// *different* test — the corrupt-line one — because which test loses a race is a
// property of the scheduler, not of the bug. The seam replaces the race with the
// exact interleaving.
func TestStreamDoesNotLoseAnEventAppendedDuringTheReplay(t *testing.T) {
	store := newStore(t)
	require.NoError(t, store.Append(ev("already-there", session.StatusWaiting, 0)))

	stream := &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond}
	stream.SetAfterReplayForTest(func() {
		// Exactly the window: the replay has been written, following has not begun.
		require.NoError(t, store.Append(ev("during-replay", session.StatusWorking, 1)))
	})

	out := runStream(t, stream)

	got := waitForEvents(t, out, 2)
	ids := make([]string, 0, len(got))
	for _, e := range got {
		ids = append(ids, e.SessionID)
	}
	assert.Contains(t, ids, "during-replay",
		"an event appended during the replay was never reported")
	assert.Equal(t, "already-there", got[0].SessionID, "the replay should still come first")
}

func TestStreamSkipsACorruptLineWithoutEndingTheWatch(t *testing.T) {
	// A killed hook process leaves a partial write. Ending the stream would take
	// every other session on that host down with it.
	store := newStore(t)
	out := runStream(t, &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond})

	f, err := os.OpenFile(store.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("{not json}\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, store.Append(ev("s-after", session.StatusIdle, 1)))

	got := waitForEvents(t, out, 1)
	assert.Equal(t, "s-after", got[0].SessionID)
}

func TestStreamStartsBeforeTheLogExists(t *testing.T) {
	// Watching a host where Claude has not run yet is normal. The stream must wait
	// rather than fail.
	store := newStore(t)
	// NewStore makes the directory but not the file; only a hook firing does that.
	_, statErr := os.Stat(store.Path)
	require.ErrorIs(t, statErr, os.ErrNotExist, "the log should not exist yet")

	out := runStream(t, &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond})

	require.NoError(t, store.Append(ev("s1", session.StatusIdle, 0)))
	got := waitForEvents(t, out, 1)
	assert.Equal(t, "s1", got[0].SessionID)
}

func TestStreamRecoversWhenTheLogIsCompactedUnderneathIt(t *testing.T) {
	// Compaction replaces the file, so the offset points past the end of a
	// different one. Re-reading from the start is safe because the registry folds
	// repeats in idempotently.
	store := newStore(t)
	require.NoError(t, store.Append(ev("s1", session.StatusWorking, 0)))

	out := runStream(t, &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond})
	waitForEvents(t, out, 1)

	// Replace the log with a shorter one, as compaction does.
	shorter, err := ev("s2", session.StatusIdle, 5).Encode()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.Path, shorter, 0o600))

	got := waitForEvents(t, out, 2)
	var ids []string
	for _, e := range got {
		ids = append(ids, e.SessionID)
	}
	assert.Contains(t, ids, "s2", "events in the replaced file must still be delivered")
}

func TestStreamWaitsForAPartiallyWrittenLine(t *testing.T) {
	// A line without its newline is a write in progress. Emitting half of it would
	// put a corrupt line on the wire.
	store := newStore(t)
	out := runStream(t, &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond})

	line, err := ev("s1", session.StatusWorking, 0).Encode()
	require.NoError(t, err)
	half := line[:len(line)/2]

	f, err := os.OpenFile(store.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.Write(half)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, out.events(t), "a half-written line must not be emitted")

	// Finish the write; now it should arrive whole.
	f, err = os.OpenFile(store.Path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.Write(line[len(line)/2:])
	require.NoError(t, err)
	require.NoError(t, f.Close())

	got := waitForEvents(t, out, 1)
	assert.Equal(t, "s1", got[0].SessionID)
}

func TestStreamHeartbeatKeepsAnIdleConnectionBusyWithoutInventingEvents(t *testing.T) {
	// A firewall that sees no traffic will reap the SSH connection. A blank line is
	// a skipped line to the reader, so it carries no meaning.
	store := newStore(t)
	out := runStream(t, &reporter.Stream{
		Store:        store,
		PollInterval: 10 * time.Millisecond,
		Heartbeat:    30 * time.Millisecond,
	})

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "\n") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Contains(t, out.String(), "\n", "no heartbeat was written")
	assert.Empty(t, out.events(t), "a heartbeat must not decode as an event")
}

func TestStreamReturnsNilWhenCancelled(t *testing.T) {
	// Being told to stop is not a failure.
	store := newStore(t)
	s := &reporter.Stream{Store: store, PollInterval: 10 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, &syncBuffer{}) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not stop")
	}
}
