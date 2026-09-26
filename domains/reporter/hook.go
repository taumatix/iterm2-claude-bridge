package reporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// hookPayload is the part of Claude Code's hook input this cares about.
//
// The field names are from Claude Code's hooks reference
// (https://code.claude.com/docs/en/hooks), which documents session_id,
// transcript_path, cwd and hook_event_name as common to every event. Everything
// else in the payload is ignored, and unknown fields are ignored rather than
// rejected, because Claude Code adds fields far more often than it removes them.
type hookPayload struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Cwd           string `json:"cwd"`
}

// ErrNotAStatusChange reports a hook event that should not move a session's
// status. Most events are like this, so it is an ordinary outcome and not a
// failure: the hook exits 0 and says nothing.
var ErrNotAStatusChange = errors.New("reporter: this hook event is not a status change")

// Hook records one status change from a Claude Code hook invocation.
type Hook struct {
	Store *Store
	Tmux  *TmuxResolver
}

// Handle reads a hook payload from r and records the resulting event.
//
// It returns [ErrNotAStatusChange] for an event that should not move the status,
// which the caller should treat as success. Any other error means the event was
// meant to be recorded and was not.
//
// A hook that fails must not break Claude: the caller exits 0 regardless, and
// this only decides what gets written.
func (h *Hook) Handle(ctx context.Context, r io.Reader) (session.Event, error) {
	body, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return session.Event{}, fmt.Errorf("reporter: reading the hook payload: %w", err)
	}

	var payload hookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return session.Event{}, fmt.Errorf("reporter: parsing the hook payload (%d bytes): %w", len(body), err)
	}
	if payload.SessionID == "" {
		return session.Event{}, errors.New("reporter: the hook payload carries no session_id")
	}

	status, ok := session.StatusFromHookEvent(payload.HookEventName)
	if !ok {
		return session.Event{}, fmt.Errorf("%w: %q", ErrNotAStatusChange, payload.HookEventName)
	}

	event := session.Event{
		SessionID: payload.SessionID,
		Status:    status,
		Cwd:       payload.Cwd,
		HookEvent: payload.HookEventName,
	}

	// A failed tmux lookup is not fatal: the status is still worth recording, and
	// the registry keeps whatever target an earlier event established. The row
	// simply is not attachable until a lookup succeeds.
	if h.Tmux != nil {
		if target, err := h.Tmux.Resolve(ctx); err == nil {
			event.Tmux = target
		}
	}

	if err := h.Store.Append(event); err != nil {
		return event, err
	}
	return event, nil
}
