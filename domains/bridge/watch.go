package bridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// Backoff bounds how fast a failing host is retried.
type Backoff struct {
	// Min is the first delay after a failure.
	Min time.Duration

	// Max is the ceiling the delay grows to.
	Max time.Duration
}

// DefaultBackoff retries quickly at first — a laptop waking from sleep should
// reconnect in seconds — and then slowly, so an unreachable host is not
// hammered.
var DefaultBackoff = Backoff{Min: time.Second, Max: 2 * time.Minute}

// Next returns the delay after n consecutive failures, doubling from Min up to
// Max. An unset Backoff falls back to [DefaultBackoff], so a zero value is
// usable rather than a zero delay that would spin.
func (b Backoff) Next(n int) time.Duration {
	min, max := b.Min, b.Max
	if min <= 0 {
		min = DefaultBackoff.Min
	}
	if max <= 0 {
		max = DefaultBackoff.Max
	}
	delay := min
	for range n {
		delay *= 2
		if delay >= max {
			return max
		}
	}
	return delay
}

// Launcher starts the process that streams one host's events.
//
// It exists so a watch can be driven by a real subprocess in tests without
// needing a reachable SSH server: what matters to this package is a command
// whose standard output is the event stream, and ssh is one of those.
type Launcher interface {
	// Start runs the command for host and returns its standard output.
	Start(ctx context.Context, host string) (stdout io.ReadCloser, wait func() error, err error)
}

// SSHLauncher runs the reporter on a remote host over ssh.
type SSHLauncher struct {
	SSH SSHOptions

	// RemoteCommand is what to run on the far side. Defaults to
	// [DefaultRemoteCommand].
	RemoteCommand string

	// Log receives the command's standard error, which is where ssh puts "host
	// unreachable" and the reporter puts its diagnostics. Without this they would
	// vanish and an unreachable host would look like a quiet one.
	Log *slog.Logger
}

// Start launches ssh for host.
func (l SSHLauncher) Start(ctx context.Context, host string) (io.ReadCloser, func() error, error) {
	program, args := l.SSH.StreamCommand(host, l.RemoteCommand)
	cmd := exec.CommandContext(ctx, program, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("bridge: connecting to %s: %w", host, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("bridge: connecting to %s: %w", host, err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("bridge: starting %s for %s: %w", program, host, err)
	}

	// Drained in the background, because a full stderr pipe would block the
	// remote's writes and stall the stream.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if l.Log != nil {
				l.Log.Warn("remote reported a problem", "host", host, "stderr", scanner.Text())
			}
		}
	}()

	// cmd.Wait closes the pipes from StderrPipe as soon as the process has exited,
	// so calling it straight away races this drain and can discard what ssh wrote
	// on its way out. That is the one case the draining exists for: an unreachable
	// host prints "no route to host" and exits immediately, and losing it leaves an
	// unreachable host looking like a quiet one.
	//
	// Waiting for the drain first cannot deadlock: the write end in this process is
	// closed by exec after Start, so the scanner reaches EOF when the child exits —
	// which is what cmd.Wait was going to block on anyway.
	wait := func() error {
		<-drained
		return cmd.Wait()
	}

	return stdout, wait, nil
}

// Watcher keeps a registry up to date from a set of hosts.
type Watcher struct {
	Registry *session.Registry
	Launcher Launcher
	Backoff  Backoff
	Log      *slog.Logger

	// ForgetGoneAfter is how long a session that has ended stays in the panel
	// before its row disappears. Long enough to notice it finished, short enough
	// not to clutter. Zero means remove it at once.
	ForgetGoneAfter time.Duration
}

// Watch follows every host until ctx is cancelled.
//
// One host being unreachable must not affect another, so each gets its own
// goroutine and its own retry schedule.
func (w *Watcher) Watch(ctx context.Context, hosts []string) {
	var wg sync.WaitGroup
	for _, host := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.watchHost(ctx, host)
		}()
	}
	wg.Wait()
}

// watchHost streams one host, reconnecting until ctx is cancelled.
func (w *Watcher) watchHost(ctx context.Context, host string) {
	failures := 0
	for ctx.Err() == nil {
		err := w.streamOnce(ctx, host)
		if ctx.Err() != nil {
			return
		}

		// The stream ending means this program no longer knows the state of that
		// host's sessions. Showing the last thing it heard would be a lie that
		// looks exactly like the truth, so the rows go.
		w.Registry.ForgetHost(host)

		failures++
		delay := w.backoff().Next(failures - 1)
		if err != nil {
			w.log().Warn("host stream ended, will retry",
				"host", host, "error", err, "retry_in", delay, "consecutive_failures", failures)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// streamOnce runs one connection to completion.
func (w *Watcher) streamOnce(ctx context.Context, host string) error {
	stdout, wait, err := w.Launcher.Start(ctx, host)
	if err != nil {
		return err
	}

	// Cancelling the context kills the command, which is not enough to unblock a
	// read: any grandchild that inherited the pipe keeps its write end open, so the
	// read waits for that process instead. Closing the pipe is what actually
	// returns from Scan, and it has to happen from another goroutine because this
	// one is inside the read.
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stdout.Close()
		case <-stopped:
		}
	}()

	readErr := w.consume(ctx, host, stdout)
	close(stopped)
	// Closed here too, so a remote still writing gets EPIPE and exits rather than
	// leaving Wait blocked. Closing twice is harmless.
	stdout.Close()
	waitErr := wait()

	if readErr != nil {
		return readErr
	}
	// A command killed because the context was cancelled is not a failure.
	if waitErr != nil && ctx.Err() == nil {
		return waitErr
	}
	return nil
}

// consume reads events from one host's stream into the registry.
func (w *Watcher) consume(ctx context.Context, host string, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	introduced := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		if !introduced {
			// A stream opens with its hello. One that opens with anything else,
			// an event or a heartbeat, is from a build that sends none.
			introduced = true
			if hello, ok := session.DecodeHello(scanner.Bytes()); ok {
				w.Registry.SetRemote(host, hello)
				continue
			}
			w.Registry.SetRemote(host, session.Hello{Protocol: 1})
		}
		e, err := session.DecodeEvent(scanner.Bytes())
		if err != nil {
			if errors.Is(err, session.ErrSkipLine) {
				// A heartbeat, keeping the connection from being reaped.
				continue
			}
			// One unreadable line must not end a watch that is otherwise working.
			w.log().Warn("skipping an unreadable line", "host", host, "error", err)
			continue
		}

		// The remote does not know what the local side calls it, and that name is
		// what has to go back into an ssh command.
		e.Host = host

		if w.Registry.Apply(e) && e.Status == session.StatusGone {
			w.scheduleForget(ctx, e.Key())
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("bridge: reading from %s: %w", host, err)
	}
	return nil
}

// scheduleForget removes an ended session's row after a delay, so a human has a
// moment to see that it finished.
func (w *Watcher) scheduleForget(ctx context.Context, key string) {
	if w.ForgetGoneAfter <= 0 {
		w.Registry.Forget(key)
		return
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(w.ForgetGoneAfter):
			// Only if it is still gone: a session id could be reported again, and
			// removing a live row would be worse than leaving a stale one.
			if s, ok := w.Registry.Lookup(key); ok && s.Status == session.StatusGone {
				w.Registry.Forget(key)
			}
		}
	}()
}

func (w *Watcher) backoff() Backoff {
	if w.Backoff.Min > 0 || w.Backoff.Max > 0 {
		return w.Backoff
	}
	return DefaultBackoff
}

func (w *Watcher) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}
