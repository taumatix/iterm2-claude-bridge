package reporter

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// DefaultPollInterval is how often Stream looks for new lines.
//
// The log is written by short-lived hook processes on the same machine, so there
// is no long-lived writer to be notified by. Polling a stat is cheap, and a
// second is well inside what a human reads as immediate — the round trip to the
// watching Mac over SSH is the same order anyway.
const DefaultPollInterval = time.Second

// Stream replays the recorded state and then follows the log, writing one JSON
// event per line.
//
// This is what the local half runs over SSH: `ssh host iterm2-claude-bridge
// stream`. Its output is the protocol, so nothing else may be written to the
// same stream — diagnostics go to stderr.
type Stream struct {
	Store *Store

	// PollInterval is how often to look for new lines. Defaults to
	// [DefaultPollInterval].
	PollInterval time.Duration

	// Heartbeat, when set, writes a blank line if nothing else has been written
	// for this long. A blank line is a skipped line to the reader, so it carries no
	// meaning beyond keeping an idle SSH connection from being reaped by a firewall
	// that sees no traffic. Zero disables it.
	Heartbeat time.Duration
}

func (s *Stream) pollInterval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return DefaultPollInterval
}

// Run writes events to w until ctx is cancelled.
//
// It returns nil on cancellation, because being told to stop is not a failure.
func (s *Stream) Run(ctx context.Context, w io.Writer) error {
	// Replayed first, so a watcher that has just connected sees every session
	// immediately rather than only those that change from now on.
	replay, err := s.Store.Replay()
	if err != nil {
		return err
	}

	out := bufio.NewWriter(w)
	for _, e := range replay {
		if err := writeEvent(out, e); err != nil {
			return err
		}
	}
	if err := out.Flush(); err != nil {
		return fmt.Errorf("reporter: writing the replay: %w", err)
	}

	// Following starts from the end of the file as it was when the replay was
	// taken, so nothing is sent twice.
	offset, err := s.size()
	if err != nil {
		return err
	}

	ticker := time.NewTicker(s.pollInterval())
	defer ticker.Stop()

	lastWrite := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		wrote, next, err := s.follow(ctx, out, offset)
		if err != nil {
			return err
		}
		offset = next

		if wrote {
			lastWrite = time.Now()
		} else if s.Heartbeat > 0 && time.Since(lastWrite) >= s.Heartbeat {
			// A blank line: the reader skips it, and the connection has seen traffic.
			if _, err := out.WriteString("\n"); err != nil {
				return fmt.Errorf("reporter: writing a heartbeat: %w", err)
			}
			lastWrite = time.Now()
		}
		if err := out.Flush(); err != nil {
			return fmt.Errorf("reporter: flushing the stream: %w", err)
		}
	}
}

// follow writes any lines added past offset and returns the new offset.
func (s *Stream) follow(ctx context.Context, w io.Writer, offset int64) (wrote bool, next int64, err error) {
	size, err := s.size()
	if err != nil {
		return false, offset, err
	}
	switch {
	case size == offset:
		return false, offset, nil
	case size < offset:
		// The log was compacted and replaced, so the old offset points past the end
		// of a different file. Starting again from the beginning re-sends the
		// current state, which the registry folds in idempotently — it ignores an
		// event older than what it holds, and a repeat of the newest is no change.
		offset = 0
	}

	f, err := os.Open(s.Store.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The log has not been created yet, or was removed. Either way there is
			// nothing past the offset, and it will reappear if a hook fires.
			return false, 0, nil
		}
		return false, offset, fmt.Errorf("reporter: opening %s: %w", s.Store.Path, err)
	}
	defer f.Close()

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false, offset, fmt.Errorf("reporter: seeking in %s: %w", s.Store.Path, err)
	}

	reader := bufio.NewReader(f)
	consumed := offset
	for {
		if ctx.Err() != nil {
			return wrote, consumed, nil
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// A line without its newline is a write still in progress. Leaving the
			// offset before it means it is read whole on the next tick.
			break
		}
		consumed += int64(len(line))

		e, decodeErr := session.DecodeEvent(line)
		if decodeErr != nil {
			// One bad line is skipped, not fatal: a killed hook process can leave a
			// partial write behind, and every session after it still matters.
			continue
		}
		if err := writeEvent(w, e); err != nil {
			return wrote, consumed, err
		}
		wrote = true
	}
	return wrote, consumed, nil
}

// size reports the log's current length, treating a missing file as empty.
func (s *Stream) size() (int64, error) {
	info, err := os.Stat(s.Store.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reporter: checking the size of %s: %w", s.Store.Path, err)
	}
	return info.Size(), nil
}

// writeEvent re-encodes an event rather than forwarding the stored bytes, so
// this build's field set is what goes out even when the file was written by
// another version.
func writeEvent(w io.Writer, e session.Event) error {
	line, err := e.Encode()
	if err != nil {
		return err
	}
	if _, err := w.Write(line); err != nil {
		return fmt.Errorf("reporter: writing an event: %w", err)
	}
	return nil
}
