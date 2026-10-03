package bridge_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	iterm2 "github.com/taumatix/iterm2-go"

	"github.com/taumatix/iterm2-claude-bridge/domains/bridge"
)

// iTerm2 restarts on every update, and until this the bridge kept its first
// connection for life: the toolbelt registration died with iTerm2 and every
// click failed until the user restarted the bridge, with nothing saying so.
// These drive a real iterm2.Connect over a real unix socket against a server
// that goes away and comes back on the same path.

func linkTo(t *testing.T, srv *itermServer) *bridge.Link {
	t.Helper()
	return &bridge.Link{
		Dial: func(ctx context.Context) (*iterm2.Conn, error) {
			return iterm2.Connect(ctx,
				iterm2.WithSocketPath(srv.path),
				iterm2.WithCredentials("cookie", "key"),
				// While the socket is gone Connect falls back to iTerm2's legacy
				// TCP port on loopback; a test must never reach a real iTerm2
				// there, so point it at a port nothing listens on.
				iterm2.WithTCPAddress("127.0.0.1:1"),
			)
		},
		OnConnect: func(ctx context.Context, term bridge.Terminal) error {
			return bridge.RegisterPanel(ctx, term, "http://127.0.0.1:9/panel", false)
		},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Retry: 50 * time.Millisecond,
	}
}

func runLink(t *testing.T, link *bridge.Link) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, link.Connect(ctx))
	done := make(chan struct{})
	go func() { link.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after its context was cancelled")
		}
		link.Close()
	})
	return cancel
}

func TestThePanelIsRegisteredAgainWhenITermComesBack(t *testing.T) {
	srv := startITermServer(t)
	runLink(t, linkTo(t, srv))
	srv.eventually(func(_ int, reg []string, _ int) bool { return len(reg) == 1 }, "the panel was never registered")

	srv.restart(200 * time.Millisecond)

	srv.eventually(func(h int, reg []string, _ int) bool { return h == 2 && len(reg) == 2 },
		"after iTerm2 came back the bridge neither reconnected nor registered its panel again")
}

// A click after the restart must reach the new connection, not the dead one
// the Opener was built with.
func TestCallsReachTheNewConnectionAfterARestart(t *testing.T) {
	srv := startITermServer(t)
	link := linkTo(t, srv)
	runLink(t, link)
	srv.restart(200 * time.Millisecond)
	srv.eventually(func(_ int, reg []string, _ int) bool { return len(reg) == 2 }, "no re-registration")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := link.ListSessions(ctx)
	require.NoError(t, err, "a call after the restart went to the dead connection")
	_, _, listed := srv.snapshot()
	assert.Equal(t, 1, listed)
}

// While iTerm2 is away a click says so at once, rather than hanging or
// failing with a socket error that names nothing the user can act on.
func TestWhileITermIsAwayACallSaysSo(t *testing.T) {
	srv := startITermServer(t)
	link := linkTo(t, srv)
	runLink(t, link)
	srv.eventually(func(_ int, reg []string, _ int) bool { return len(reg) == 1 }, "never registered")

	srv.stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	require.Eventually(t, func() bool {
		_, err = link.ListSessions(ctx)
		return errors.Is(err, bridge.ErrITermUnavailable)
	}, 5*time.Second, 20*time.Millisecond, "last error: %v", err)
}

// The first connection still fails fast, with the hint about the API setting:
// a bridge started with the API switched off should not sit retrying in silence.
func TestTheFirstConnectionFailureIsReturned(t *testing.T) {
	srv := startITermServer(t)
	srv.stop()

	err := linkTo(t, srv).Connect(context.Background())

	require.Error(t, err)
}
