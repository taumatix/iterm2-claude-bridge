package session_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

func TestStatusFromHookEventUsesClaudeCodesOwnEventNames(t *testing.T) {
	// The names are Claude Code's, spelled as settings.json needs them. Getting one
	// wrong means a hook that never fires, which looks like a session that never
	// changes state.
	cases := map[string]session.Status{
		"SessionStart":      session.StatusIdle,
		"UserPromptSubmit":  session.StatusWorking,
		"PreToolUse":        session.StatusWorking,
		"PostToolUse":       session.StatusWorking,
		"Notification":      session.StatusWaiting,
		"PermissionRequest": session.StatusWaiting,
		"Stop":              session.StatusIdle,
		"StopFailure":       session.StatusIdle,
		"SessionEnd":        session.StatusGone,
	}
	for event, want := range cases {
		t.Run(event, func(t *testing.T) {
			got, ok := session.StatusFromHookEvent(event)
			require.True(t, ok, "%s should map to a status", event)
			assert.Equal(t, want, got)
		})
	}
}

func TestStatusFromHookEventIgnoresEventsThatAreNotStatusChanges(t *testing.T) {
	// Most hooks say nothing about whether a human is needed. Mapping them all
	// would make every session flicker, so an unlisted event changes nothing.
	for _, event := range []string{
		"PostToolBatch", "MessageDisplay", "PreCompact", "PostCompact",
		"InstructionsLoaded", "CwdChanged", "FileChanged", "SubagentStart",
		"", "NotAnEvent",
	} {
		t.Run(event, func(t *testing.T) {
			_, ok := session.StatusFromHookEvent(event)
			assert.False(t, ok)
		})
	}
}

func TestHookEventsReportedIsWhatTheInstallerShouldWrite(t *testing.T) {
	events := session.HookEventsReported()
	assert.Len(t, events, 9)
	assert.Contains(t, events, "SessionEnd")
	assert.IsIncreasing(t, events, "sorted, so a generated settings.json does not churn")

	// Every reported event must actually map, or the installer would register a
	// hook this build then ignores.
	for _, event := range events {
		_, ok := session.StatusFromHookEvent(event)
		assert.True(t, ok, "%s is advertised but not mapped", event)
	}
}

func TestEventRoundTripsThroughTheWire(t *testing.T) {
	want := session.Event{
		SessionID: "abc123",
		Status:    session.StatusWorking,
		At:        time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
		Tmux:      session.TmuxTarget{Session: "build", Window: "@3", Pane: "%7"},
		Cwd:       "/srv/app",
		HookEvent: "PreToolUse",
	}

	line, err := want.Encode()
	require.NoError(t, err)
	assert.Equal(t, byte('\n'), line[len(line)-1], "the stream is newline-delimited")

	got, err := session.DecodeEvent(line)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestDecodeEventSkipsBlankLines(t *testing.T) {
	for _, line := range []string{"", "   ", "\n", "\t"} {
		_, err := session.DecodeEvent([]byte(line))
		assert.ErrorIs(t, err, session.ErrSkipLine)
	}
}

func TestDecodeEventRejectsAnEventMissingWhatEverythingElseNeeds(t *testing.T) {
	base := map[string]any{
		"session_id": "abc",
		"status":     "working",
		"at":         "2026-09-26T12:00:00Z",
	}
	for _, missing := range []string{"session_id", "status", "at"} {
		t.Run("no "+missing, func(t *testing.T) {
			payload := map[string]any{}
			for k, v := range base {
				if k != missing {
					payload[k] = v
				}
			}
			line, err := json.Marshal(payload)
			require.NoError(t, err)

			_, err = session.DecodeEvent(line)
			require.Error(t, err)
			assert.Contains(t, err.Error(), missing)
		})
	}
}

func TestDecodeEventKeepsAnUnknownStatusFromANewerRemote(t *testing.T) {
	// The remote half is upgraded by whoever administers that host. A fourth state
	// should produce a row this build renders plainly, not a dropped session.
	line := []byte(`{"session_id":"abc","status":"compacting","at":"2026-09-26T12:00:00Z"}`)

	got, err := session.DecodeEvent(line)
	require.NoError(t, err)
	assert.Equal(t, session.Status("compacting"), got.Status)
	assert.False(t, got.Status.Valid())
}

func TestDecodeEventIgnoresFieldsThisBuildDoesNotKnow(t *testing.T) {
	line := []byte(`{"session_id":"abc","status":"idle","at":"2026-09-26T12:00:00Z","model":"opus-5","nested":{"a":1}}`)

	got, err := session.DecodeEvent(line)
	require.NoError(t, err)
	assert.Equal(t, "abc", got.SessionID)
}

func TestDecodeEventReportsCorruptJSONWithoutClaimingItIsSkippable(t *testing.T) {
	// One partial write must be reportable and survivable, but it is not the same
	// as a blank line: the caller logs it rather than passing over it silently.
	_, err := session.DecodeEvent([]byte(`{"session_id":"abc",`))
	require.Error(t, err)
	assert.NotErrorIs(t, err, session.ErrSkipLine)
}

func TestTmuxTargetZeroMeansNothingToAttachTo(t *testing.T) {
	assert.True(t, session.TmuxTarget{}.Zero())
	assert.True(t, session.TmuxTarget{Window: "@1"}.Zero(), "a window without a session is not a target")
	assert.False(t, session.TmuxTarget{Session: "build"}.Zero())
}

func TestParseStatus(t *testing.T) {
	got, err := session.ParseStatus("waiting")
	require.NoError(t, err)
	assert.Equal(t, session.StatusWaiting, got)

	_, err = session.ParseStatus("nonsense")
	assert.Error(t, err)
}
