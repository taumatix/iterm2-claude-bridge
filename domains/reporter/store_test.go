package reporter_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/reporter"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

var storeBase = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *reporter.Store {
	t.Helper()
	s, err := reporter.NewStore(t.TempDir())
	require.NoError(t, err)
	return s
}

func ev(id string, status session.Status, secondsIn int) session.Event {
	return session.Event{
		SessionID: id,
		Status:    status,
		At:        storeBase.Add(time.Duration(secondsIn) * time.Second),
	}
}

func TestStoreReplaysWhatWasAppended(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.Append(ev("s1", session.StatusWorking, 0)))

	got, err := s.Replay()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "s1", got[0].SessionID)
	assert.Equal(t, session.StatusWorking, got[0].Status)
}

func TestReplayIsEmptyBeforeAnyHookHasFired(t *testing.T) {
	// A watcher connecting to a host where Claude has never run is normal, not an
	// error.
	s := newStore(t)
	got, err := s.Replay()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReplayKeepsOnlyTheNewestEventPerSession(t *testing.T) {
	// Replaying is how a reconnecting watcher catches up, so it must yield current
	// state rather than history.
	s := newStore(t)
	require.NoError(t, s.Append(ev("s1", session.StatusWorking, 0)))
	require.NoError(t, s.Append(ev("s1", session.StatusWaiting, 1)))
	require.NoError(t, s.Append(ev("s1", session.StatusIdle, 2)))
	require.NoError(t, s.Append(ev("s2", session.StatusWorking, 1)))

	got, err := s.Replay()
	require.NoError(t, err)
	require.Len(t, got, 2)

	byID := map[string]session.Event{}
	for _, e := range got {
		byID[e.SessionID] = e
	}
	assert.Equal(t, session.StatusIdle, byID["s1"].Status)
	assert.Equal(t, session.StatusWorking, byID["s2"].Status)
}

func TestReplayIsOrderedOldestFirst(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.Append(ev("s-late", session.StatusIdle, 10)))
	require.NoError(t, s.Append(ev("s-early", session.StatusIdle, 1)))

	got, err := s.Replay()
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "s-early", got[0].SessionID)
	assert.Equal(t, "s-late", got[1].SessionID)
}

func TestAppendStampsAnUndatedEvent(t *testing.T) {
	// An undated event would be rejected by the stream's decoder, so it is dated
	// here rather than left for the caller to remember.
	s := newStore(t)
	s.Now = func() time.Time { return storeBase }

	require.NoError(t, s.Append(session.Event{SessionID: "s1", Status: session.StatusIdle}))

	got, err := s.Replay()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, storeBase, got[0].At.UTC())
}

func TestAppendRejectsAnEventNothingCouldUse(t *testing.T) {
	s := newStore(t)
	assert.Error(t, s.Append(session.Event{Status: session.StatusIdle}), "no session id")
}

func TestReplaySkipsACorruptLineAndKeepsTheRest(t *testing.T) {
	// A hook process killed mid-write leaves a partial line. Every session after
	// it still matters.
	s := newStore(t)
	require.NoError(t, s.Append(ev("s1", session.StatusWorking, 0)))

	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("{\"session_id\":\"truncated\"\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, s.Append(ev("s2", session.StatusIdle, 1)))

	got, err := s.Replay()
	require.NoError(t, err)
	require.Len(t, got, 2, "the corrupt line should be skipped, not fatal")
}

func TestStoreCreatesItsDirectoryPrivately(t *testing.T) {
	// The log carries working directory paths, which say something about what is
	// being worked on.
	dir := filepath.Join(t.TempDir(), "nested", "state")
	s, err := reporter.NewStore(dir)
	require.NoError(t, err)
	require.NoError(t, s.Append(ev("s1", session.StatusIdle, 0)))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	fileInfo, err := os.Stat(s.Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
}

func TestAppendCompactsOnceTheLogGrowsPastItsLimit(t *testing.T) {
	// Without this, a week of PreToolUse events would be replayed to every new
	// watcher. The property being kept is that replaying the file yields current
	// state.
	s := newStore(t)

	// A long cwd makes each line big enough to cross the 256 KiB threshold without
	// writing an unreasonable number of events.
	longCwd := "/srv/" + strings.Repeat("deep/", 400)
	for i := range 700 {
		e := ev("s1", session.StatusWorking, i)
		e.Cwd = longCwd
		require.NoError(t, s.Append(e))
	}

	info, err := os.Stat(s.Path)
	require.NoError(t, err)
	assert.Less(t, info.Size(), int64(256<<10),
		"the log should have been compacted rather than growing without bound")

	got, err := s.Replay()
	require.NoError(t, err)
	require.Len(t, got, 1, "compaction keeps one event per session")
	assert.Equal(t, session.StatusWorking, got[0].Status)
	// The newest event survives, not an arbitrary one.
	assert.Equal(t, storeBase.Add(699*time.Second), got[0].At.UTC())
}

func TestCompactionKeepsEverySessionsNewestEvent(t *testing.T) {
	s := newStore(t)
	longCwd := "/srv/" + strings.Repeat("deep/", 400)

	for i := range 400 {
		for _, id := range []string{"s1", "s2", "s3"} {
			e := ev(id, session.StatusWorking, i)
			e.Cwd = longCwd
			require.NoError(t, s.Append(e))
		}
	}
	// A final distinct status per session, so the test can tell which survived.
	for _, id := range []string{"s1", "s2", "s3"} {
		require.NoError(t, s.Append(ev(id, session.StatusWaiting, 9999)))
	}

	got, err := s.Replay()
	require.NoError(t, err)
	require.Len(t, got, 3)
	for _, e := range got {
		assert.Equal(t, session.StatusWaiting, e.Status, "session %s", e.SessionID)
	}
}

func TestConcurrentHookProcessesDoNotSplitEachOthersLines(t *testing.T) {
	// Each hook invocation is a separate short-lived process with no coordination
	// beyond the filesystem, so several can append at once. A split line would
	// lose both events.
	s := newStore(t)

	const writers, each = 8, 40
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				e := ev(fmt.Sprintf("s-%d", w), session.StatusWorking, i)
				e.Cwd = "/srv/app"
				assert.NoError(t, s.Append(e))
			}
		}()
	}
	wg.Wait()

	// Every line must still parse, and every writer must be represented.
	got, err := s.Replay()
	require.NoError(t, err)
	assert.Len(t, got, writers)

	body, err := os.ReadFile(s.Path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	assert.Len(t, lines, writers*each, "no line should have been lost or merged")
	for i, line := range lines {
		_, err := session.DecodeEvent([]byte(line))
		assert.NoError(t, err, "line %d is not a whole event: %q", i, line)
	}
}
