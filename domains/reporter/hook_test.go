package reporter_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/reporter"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// realPayload is a Claude Code hook payload with the fields the hooks reference
// documents as common to every event, plus the event-specific ones PreToolUse
// carries. Kept whole rather than trimmed, because ignoring what it does not
// need is part of what the hook has to do.
const realPayload = `{
  "session_id": "abc123",
  "prompt_id": "550e8400-e29b-41d4-a716-446655440000",
  "transcript_path": "/home/user/.claude/projects/x/transcript.jsonl",
  "cwd": "/home/user/my-project",
  "scratchpad_dir": "/tmp/claude-1000/x/abc123/scratchpad",
  "permission_mode": "default",
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": {"command": "npm test", "timeout": 120000},
  "tool_use_id": "toolu_01ABC123"
}`

func newHook(t *testing.T) (*reporter.Hook, *reporter.Store) {
	t.Helper()
	store := newStore(t)
	store.Now = func() time.Time { return storeBase }
	return &reporter.Hook{Store: store}, store
}

func TestHookRecordsARealClaudeCodePayload(t *testing.T) {
	h, store := newHook(t)

	event, err := h.Handle(context.Background(), strings.NewReader(realPayload))
	require.NoError(t, err)

	assert.Equal(t, "abc123", event.SessionID)
	assert.Equal(t, session.StatusWorking, event.Status)
	assert.Equal(t, "/home/user/my-project", event.Cwd)
	assert.Equal(t, "PreToolUse", event.HookEvent)

	// Recorded, not merely returned.
	stored, err := store.Replay()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "abc123", stored[0].SessionID)
}

func TestHookIgnoresAnEventThatIsNotAStatusChange(t *testing.T) {
	// Most hook events say nothing about whether a human is needed. The caller
	// treats this as success.
	h, store := newHook(t)

	_, err := h.Handle(context.Background(),
		strings.NewReader(`{"session_id":"abc","hook_event_name":"PostToolBatch"}`))
	require.ErrorIs(t, err, reporter.ErrNotAStatusChange)

	stored, err := store.Replay()
	require.NoError(t, err)
	assert.Empty(t, stored, "nothing should have been recorded")
}

func TestHookMapsEachReportedEventToItsStatus(t *testing.T) {
	for _, tc := range []struct {
		event string
		want  session.Status
	}{
		{"SessionStart", session.StatusIdle},
		{"UserPromptSubmit", session.StatusWorking},
		{"Notification", session.StatusWaiting},
		{"PermissionRequest", session.StatusWaiting},
		{"Stop", session.StatusIdle},
		{"SessionEnd", session.StatusGone},
	} {
		t.Run(tc.event, func(t *testing.T) {
			h, _ := newHook(t)
			payload := `{"session_id":"abc","hook_event_name":"` + tc.event + `","cwd":"/srv"}`

			event, err := h.Handle(context.Background(), strings.NewReader(payload))
			require.NoError(t, err)
			assert.Equal(t, tc.want, event.Status)
		})
	}
}

func TestHookRejectsAPayloadWithNoSessionID(t *testing.T) {
	// Without it there is nothing to attribute the status to.
	h, _ := newHook(t)

	_, err := h.Handle(context.Background(), strings.NewReader(`{"hook_event_name":"Stop"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session_id")
}

func TestHookRejectsAPayloadThatIsNotJSON(t *testing.T) {
	h, _ := newHook(t)

	_, err := h.Handle(context.Background(), strings.NewReader("not json at all"))
	require.Error(t, err)
	// The length is worth reporting: an empty payload and a malformed one are
	// different problems.
	assert.Contains(t, err.Error(), "15 bytes")
}

func TestHookIgnoresFieldsClaudeCodeAddsLater(t *testing.T) {
	// Claude Code adds hook payload fields far more often than it removes them.
	h, _ := newHook(t)
	payload := `{"session_id":"abc","hook_event_name":"Stop","future_field":{"nested":[1,2]}}`

	event, err := h.Handle(context.Background(), strings.NewReader(payload))
	require.NoError(t, err)
	assert.Equal(t, session.StatusIdle, event.Status)
}

func TestHookRecordsTheStatusEvenWhenTmuxLookupFails(t *testing.T) {
	// The status is still worth having: the registry keeps whatever target an
	// earlier event established, and the row is simply not attachable until a
	// lookup succeeds.
	store := newStore(t)
	h := &reporter.Hook{
		Store: store,
		// TMUX set with no TMUX_PANE, which Resolve reports as an error.
		Tmux: &reporter.TmuxResolver{
			Runner: &fakeRunner{},
			Env:    envFrom(map[string]string{"TMUX": "set"}),
		},
	}

	event, err := h.Handle(context.Background(), strings.NewReader(realPayload))
	require.NoError(t, err)
	assert.True(t, event.Tmux.Zero())
	assert.Equal(t, session.StatusWorking, event.Status)

	stored, err := store.Replay()
	require.NoError(t, err)
	require.Len(t, stored, 1)
}

func TestHookAttachesTheTmuxTargetWhenItResolves(t *testing.T) {
	store := newStore(t)
	h := &reporter.Hook{
		Store: store,
		Tmux: &reporter.TmuxResolver{
			Runner: &fakeRunner{stdout: "build|@3|%7"},
			Env:    envFrom(map[string]string{"TMUX": "set", "TMUX_PANE": "%7"}),
		},
	}

	event, err := h.Handle(context.Background(), strings.NewReader(realPayload))
	require.NoError(t, err)
	assert.Equal(t, "build", event.Tmux.Session)
	assert.Equal(t, "%7", event.Tmux.Pane)

	stored, err := store.Replay()
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, "build", stored[0].Tmux.Session, "the target must survive the round trip")
}
