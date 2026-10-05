package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
)

// ErrITermUnavailable is what a [Link] answers while it has no connection: iTerm2
// quit or is restarting, and the link is reconnecting in the background.
var ErrITermUnavailable = errors.New("bridge: iTerm2 is not connected (is it restarting?); retrying in the background")

// Link is a connection to iTerm2 that survives iTerm2 restarting, which it does
// on every update.
//
// A connection cannot be revived — its cookie is spent — so when one ends Link
// dials a fresh one and runs OnConnect on it again, which is where the toolbelt
// panel is re-registered: iTerm2 forgets a tool when it quits. Link is itself a
// [Terminal], so an [Opener] built on it always talks to the current connection.
type Link struct {
	// Dial makes one connection attempt. Each call must obtain fresh
	// credentials; iterm2.Connect with default credentials does, since
	// iterm2-go v0.2.0.
	Dial func(ctx context.Context) (*iterm2.Conn, error)

	// OnConnect runs on every new connection before it is used, the first
	// included. An error from it fails that connection like a dial error.
	OnConnect func(ctx context.Context, term Terminal) error

	// Log receives the disconnects and reconnects. nil uses the default logger.
	Log *slog.Logger

	// Retry is the first wait between attempts while iTerm2 is away. It doubles
	// to a ceiling of 30s. Zero means one second.
	Retry time.Duration

	mu   sync.RWMutex
	conn *iterm2.Conn
}

const maxLinkRetry = 30 * time.Second

// linkWarnAfter is how many reconnect attempts fail before Link says so at
// warning level: about half a minute at the default one-second start, longer
// than iTerm2 takes to restart after an update.
const linkWarnAfter = 5

// Connect makes the first connection and returns its error, so a bridge started
// with the API switched off says so at once instead of retrying in silence.
func (l *Link) Connect(ctx context.Context) error {
	conn, err := l.dialOnce(ctx)
	if err != nil {
		return err
	}
	l.set(conn)
	return nil
}

// Run watches the connection [Link.Connect] made and replaces it whenever it
// ends, until ctx is cancelled. Calls made while it is reconnecting fail with
// [ErrITermUnavailable].
func (l *Link) Run(ctx context.Context) {
	for {
		conn := l.current()
		if conn == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-conn.Done():
		}
		l.set(nil)
		l.log().Warn("lost the connection to iTerm2; reconnecting", "error", conn.Err())

		next, ok := l.redial(ctx)
		if !ok {
			return
		}
		l.set(next)
		l.log().Info("reconnected to iTerm2")
	}
}

// Close closes the current connection, if there is one.
func (l *Link) Close() {
	if conn := l.current(); conn != nil {
		_ = conn.Close()
	}
	l.set(nil)
}

// redial retries with a doubling wait until a connection succeeds or ctx ends.
func (l *Link) redial(ctx context.Context) (*iterm2.Conn, bool) {
	wait := l.Retry
	if wait <= 0 {
		wait = time.Second
	}
	for failures := 1; ; failures++ {
		conn, err := l.dialOnce(ctx)
		if err == nil {
			return conn, true
		}
		if failures == linkWarnAfter {
			// The panel is gone from the toolbelt, so the log is the only
			// place left to say so. Once, not every 30s.
			l.log().Warn("still cannot reach iTerm2; the panel stays away until it can. "+
				"Check Settings > General > Magic > Enable Python API, and that macOS lets this "+
				"program control iTerm2 (System Settings > Privacy & Security > Automation)",
				"error", err, "attempts", failures)
		}
		l.log().Debug("iTerm2 not back yet", "error", err, "retry_in", wait)
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(wait):
		}
		wait = min(wait*2, maxLinkRetry)
	}
}

func (l *Link) dialOnce(ctx context.Context) (*iterm2.Conn, error) {
	conn, err := l.Dial(ctx)
	if err != nil {
		return nil, err
	}
	if l.OnConnect != nil {
		if err := l.OnConnect(ctx, conn); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("setting up the iTerm2 connection: %w", err)
		}
	}
	return conn, nil
}

func (l *Link) set(conn *iterm2.Conn) {
	l.mu.Lock()
	l.conn = conn
	l.mu.Unlock()
}

func (l *Link) current() *iterm2.Conn {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.conn
}

func (l *Link) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.Default()
}

// terminal returns the live connection, or ErrITermUnavailable.
func (l *Link) terminal() (Terminal, error) {
	if conn := l.current(); conn != nil {
		return conn, nil
	}
	return nil, ErrITermUnavailable
}

var _ Terminal = (*Link)(nil)

func (l *Link) ListSessions(ctx context.Context) (*iterm2.Hierarchy, error) {
	t, err := l.terminal()
	if err != nil {
		return nil, err
	}
	return t.ListSessions(ctx)
}

func (l *Link) CreateTab(ctx context.Context, opts iterm2.CreateTabOptions) (*iterm2.NewTab, error) {
	t, err := l.terminal()
	if err != nil {
		return nil, err
	}
	return t.CreateTab(ctx, opts)
}

func (l *Link) GetStringVariable(ctx context.Context, scope iterm2.VariableScope, identifier, name string) (string, bool, error) {
	t, err := l.terminal()
	if err != nil {
		return "", false, err
	}
	return t.GetStringVariable(ctx, scope, identifier, name)
}

func (l *Link) SetStringVariable(ctx context.Context, scope iterm2.VariableScope, identifier, name, value string) error {
	t, err := l.terminal()
	if err != nil {
		return err
	}
	return t.SetStringVariable(ctx, scope, identifier, name, value)
}

func (l *Link) Do(ctx context.Context, req *apipb.ClientOriginatedMessage) (*apipb.ServerOriginatedMessage, error) {
	t, err := l.terminal()
	if err != nil {
		return nil, err
	}
	return t.Do(ctx, req)
}
