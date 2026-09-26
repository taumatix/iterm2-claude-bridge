package session_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

var base = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// event builds an event at an offset from a fixed base time, so ordering in a
// test is readable.
func event(host, id string, status session.Status, secondsIn int) session.Event {
	return session.Event{
		Host:      host,
		SessionID: id,
		Status:    status,
		At:        base.Add(time.Duration(secondsIn) * time.Second),
	}
}

func TestRegistryTracksTheLatestStatus(t *testing.T) {
	r := session.NewRegistry()

	assert.True(t, r.Apply(event("box", "s1", session.StatusIdle, 0)))
	assert.True(t, r.Apply(event("box", "s1", session.StatusWorking, 1)))

	sessions := r.Sessions()
	require.Len(t, sessions, 1)
	assert.Equal(t, session.StatusWorking, sessions[0].Status)
	assert.Equal(t, base.Add(time.Second), sessions[0].Since)
}

func TestRegistryReportsNoChangeForAStatusThatDidNotChange(t *testing.T) {
	// A working session fires PreToolUse and PostToolUse repeatedly. Waking the
	// panel for each would re-render it dozens of times a second.
	r := session.NewRegistry()
	require.True(t, r.Apply(event("box", "s1", session.StatusWorking, 0)))

	assert.False(t, r.Apply(event("box", "s1", session.StatusWorking, 1)))
	assert.False(t, r.Apply(event("box", "s1", session.StatusWorking, 2)))

	// Still recorded, so a later event is not mistaken for an older one.
	got, ok := r.Lookup("box\x00s1")
	require.True(t, ok)
	assert.Equal(t, base.Add(2*time.Second), got.Since)
}

func TestRegistryIgnoresAnEventOlderThanWhatItHolds(t *testing.T) {
	// The remote half replays recent history when a stream reconnects, so
	// duplicates and out-of-order arrivals are normal.
	r := session.NewRegistry()
	require.True(t, r.Apply(event("box", "s1", session.StatusIdle, 10)))

	assert.False(t, r.Apply(event("box", "s1", session.StatusWorking, 5)))

	got, _ := r.Lookup("box\x00s1")
	assert.Equal(t, session.StatusIdle, got.Status, "the older event must not win")
}

func TestRegistryKeepsTmuxAndCwdWhenALaterEventOmitsThem(t *testing.T) {
	// Not every hook payload carries a cwd, and tmux lookup can fail transiently.
	// Dropping what an earlier event established would make the row stop being
	// attachable for no reason.
	r := session.NewRegistry()

	first := event("box", "s1", session.StatusWorking, 0)
	first.Tmux = session.TmuxTarget{Session: "build", Window: "@3"}
	first.Cwd = "/srv/app"
	require.True(t, r.Apply(first))

	require.True(t, r.Apply(event("box", "s1", session.StatusIdle, 1)))

	got, _ := r.Lookup("box\x00s1")
	assert.Equal(t, "build", got.Tmux.Session)
	assert.Equal(t, "/srv/app", got.Cwd)
	assert.True(t, got.Attachable())
}

func TestRegistrySeparatesTheSameSessionIDOnDifferentHosts(t *testing.T) {
	// Claude's session id is unique per machine, not between machines.
	r := session.NewRegistry()
	require.True(t, r.Apply(event("box-a", "s1", session.StatusWorking, 0)))
	require.True(t, r.Apply(event("box-b", "s1", session.StatusIdle, 0)))

	assert.Len(t, r.Sessions(), 2)
}

func TestRegistryOrdersSessionsByAttentionThenStably(t *testing.T) {
	r := session.NewRegistry()

	idle := event("box-a", "s-idle", session.StatusIdle, 0)
	idle.Cwd = "/a"
	working := event("box-b", "s-working", session.StatusWorking, 0)
	working.Cwd = "/b"
	waiting := event("box-c", "s-waiting", session.StatusWaiting, 0)
	waiting.Cwd = "/c"

	// Applied in the wrong order on purpose.
	for _, e := range []session.Event{idle, working, waiting} {
		require.True(t, r.Apply(e))
	}

	var ids []string
	for _, s := range r.Sessions() {
		ids = append(ids, s.SessionID)
	}
	assert.Equal(t, []string{"s-waiting", "s-working", "s-idle"}, ids,
		"the sessions wanting a human come first")
}

func TestRegistrySortsAnUnknownStatusLastWithoutDroppingIt(t *testing.T) {
	// A newer remote's fourth state must be visible, but must not displace a
	// session that is known to need attention.
	r := session.NewRegistry()
	require.True(t, r.Apply(event("box", "s-waiting", session.StatusWaiting, 0)))
	require.True(t, r.Apply(event("box", "s-new", session.Status("compacting"), 0)))

	sessions := r.Sessions()
	require.Len(t, sessions, 2)
	assert.Equal(t, "s-waiting", sessions[0].SessionID)
	assert.Equal(t, "s-new", sessions[1].SessionID)
}

func TestRegistryIgnoresAnUnusableEvent(t *testing.T) {
	r := session.NewRegistry()
	assert.False(t, r.Apply(session.Event{Host: "box"}), "no session id, status or time")
	assert.Empty(t, r.Sessions())
}

func TestForgetHostDropsItsSessionsAndNothingElse(t *testing.T) {
	// When a host's stream ends, its sessions may still be running but nothing
	// here knows their state; a stale status is worse than no row.
	r := session.NewRegistry()
	require.True(t, r.Apply(event("box-a", "s1", session.StatusWorking, 0)))
	require.True(t, r.Apply(event("box-a", "s2", session.StatusIdle, 0)))
	require.True(t, r.Apply(event("box-b", "s3", session.StatusIdle, 0)))

	r.ForgetHost("box-a")

	sessions := r.Sessions()
	require.Len(t, sessions, 1)
	assert.Equal(t, "box-b", sessions[0].Host)
}

func TestForgetRemovesOneSession(t *testing.T) {
	r := session.NewRegistry()
	require.True(t, r.Apply(event("box", "s1", session.StatusGone, 0)))

	r.Forget("box\x00s1")
	assert.Empty(t, r.Sessions())

	// Forgetting something already gone is harmless.
	r.Forget("box\x00s1")
}

func TestChangedIsClosedOnAVisibleChangeOnly(t *testing.T) {
	r := session.NewRegistry()
	require.True(t, r.Apply(event("box", "s1", session.StatusWorking, 0)))

	waiter := r.Changed()
	// Same status: nothing a watcher would see, so nothing is woken.
	require.False(t, r.Apply(event("box", "s1", session.StatusWorking, 1)))
	select {
	case <-waiter:
		t.Fatal("a repeated status woke the panel")
	case <-time.After(50 * time.Millisecond):
	}

	require.True(t, r.Apply(event("box", "s1", session.StatusIdle, 2)))
	select {
	case <-waiter:
	case <-time.After(time.Second):
		t.Fatal("a status change did not wake the panel")
	}
}

func TestChangedIsWokenByForget(t *testing.T) {
	r := session.NewRegistry()
	require.True(t, r.Apply(event("box", "s1", session.StatusGone, 0)))

	waiter := r.Changed()
	r.Forget("box\x00s1")

	select {
	case <-waiter:
	case <-time.After(time.Second):
		t.Fatal("removing a session did not wake the panel")
	}
}

func TestAttachableNeedsATmuxTargetAndALiveSession(t *testing.T) {
	withTmux := func(status session.Status) session.Session {
		return session.Session{
			Tmux:   session.TmuxTarget{Session: "build"},
			Status: status,
		}
	}
	assert.True(t, withTmux(session.StatusWorking).Attachable())
	assert.True(t, withTmux(session.StatusIdle).Attachable())
	// Nothing to attach to once the session has ended.
	assert.False(t, withTmux(session.StatusGone).Attachable())
	// Claude not running under tmux has nowhere for a tab to go.
	assert.False(t, session.Session{Status: session.StatusWorking}.Attachable())
}

func TestRegistryIsSafeUnderConcurrentUse(t *testing.T) {
	// One goroutine per host writes while the panel renders and clicks read.
	r := session.NewRegistry()
	done := make(chan struct{})

	for _, host := range []string{"a", "b", "c"} {
		go func() {
			for i := range 200 {
				status := session.StatusWorking
				if i%2 == 0 {
					status = session.StatusIdle
				}
				r.Apply(event(host, "s1", status, i))
			}
			done <- struct{}{}
		}()
	}
	go func() {
		for range 400 {
			_ = r.Sessions()
			_, _ = r.Lookup("a\x00s1")
			_ = r.Changed()
		}
		done <- struct{}{}
	}()

	for range 4 {
		<-done
	}
	assert.Len(t, r.Sessions(), 3)
}
