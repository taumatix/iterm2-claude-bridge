package session_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

func TestAHelloRoundTrips(t *testing.T) {
	line, err := session.Hello{Protocol: 2, Version: "v0.5.0", CheckProcesses: true}.Encode()
	require.NoError(t, err)
	got, ok := session.DecodeHello(line)
	require.True(t, ok)
	assert.Equal(t, session.Hello{Protocol: 2, Version: "v0.5.0", CheckProcesses: true}, got)
}

// An older watcher reads the hello with DecodeEvent. It must be refused there,
// not taken for an event, so the older watcher skips it and reads on.
func TestAnOlderWatcherCannotMistakeTheHelloForAnEvent(t *testing.T) {
	line, err := session.Hello{Protocol: 2}.Encode()
	require.NoError(t, err)
	_, err = session.DecodeEvent(line)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, session.ErrSkipLine)
}

func TestAnEventIsNotAHello(t *testing.T) {
	_, ok := session.DecodeHello([]byte(`{"session_id":"s1","status":"idle","at":"2026-10-04T00:00:00Z"}`))
	assert.False(t, ok)
}
