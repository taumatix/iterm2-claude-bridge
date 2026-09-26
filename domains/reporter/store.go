package reporter

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// DefaultStateDir is where the store lives when nothing says otherwise.
const DefaultStateDir = ".iterm2-claude-bridge"

// eventFileName is the compacted log inside the state directory.
const eventFileName = "events.jsonl"

// compactAboveBytes is when Append rewrites the log keeping one event per
// session.
//
// The log is a *compacted* log: replaying it from the start yields the current
// state of every session, which is what lets a reconnecting stream catch up by
// reading a file rather than by asking a question. Compaction is what keeps that
// property affordable — without it a week of PreToolUse events would be replayed
// to every new watcher.
const compactAboveBytes = 256 << 10

// Store is the append-only-ish record of status changes on this machine.
//
// Several Claude Code sessions run concurrently and each hook invocation is a
// separate short-lived process, so writes come from unrelated processes with no
// coordination beyond the filesystem. Append therefore opens with O_APPEND and
// writes one line in one call, which is atomic for a line this short on both
// Linux and macOS, rather than holding a lock across a read-modify-write.
type Store struct {
	// Path is the log file.
	Path string

	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
}

// NewStore returns a store under dir, creating the directory if needed.
//
// An empty dir means $HOME/.iterm2-claude-bridge. It is deliberately not under
// ~/.claude: that directory belongs to Claude Code, and writing a third party's
// state into it invites a future version to tidy it away.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("reporter: finding the home directory: %w", err)
		}
		dir = filepath.Join(home, DefaultStateDir)
	}
	// 0700: the log carries working directory paths, which say something about
	// what is being worked on.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("reporter: creating %s: %w", dir, err)
	}
	return &Store{Path: filepath.Join(dir, eventFileName)}, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Append records one event.
//
// The event's time is set here if it has none, so a caller cannot record
// something the stream will reject for being undated.
func (s *Store) Append(e session.Event) error {
	if e.At.IsZero() {
		e.At = s.now().UTC()
	}
	if err := e.Validate(); err != nil {
		return err
	}

	line, err := e.Encode()
	if err != nil {
		return err
	}

	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reporter: opening %s: %w", s.Path, err)
	}
	// One Write of one line under O_APPEND, so two hook processes interleaving
	// cannot split each other's lines.
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fmt.Errorf("reporter: appending to %s: %w", s.Path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("reporter: closing %s: %w", s.Path, err)
	}

	return s.compactIfLarge()
}

// Replay returns the events needed to reconstruct the current state, oldest
// first, with at most one per session.
//
// A line that will not parse is skipped rather than fatal: one truncated write
// from a killed hook process must not make every session invisible.
func (s *Store) Replay() ([]session.Event, error) {
	events, err := s.readAll()
	if err != nil {
		return nil, err
	}
	return latestPerSession(events), nil
}

// readAll parses every line in the log, skipping what it cannot read.
func (s *Store) readAll() ([]session.Event, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No hook has fired yet. An empty history, not a problem.
			return nil, nil
		}
		return nil, fmt.Errorf("reporter: opening %s: %w", s.Path, err)
	}
	defer f.Close()

	var events []session.Event
	scanner := bufio.NewScanner(f)
	// A cwd can be long, and the default 64 KiB token limit would turn one huge
	// line into a scan error that hides every line after it.
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		e, err := session.DecodeEvent(scanner.Bytes())
		if err != nil {
			continue
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reporter: reading %s: %w", s.Path, err)
	}
	return events, nil
}

// latestPerSession keeps the newest event for each session, in time order.
func latestPerSession(events []session.Event) []session.Event {
	newest := make(map[string]session.Event, len(events))
	for _, e := range events {
		if held, ok := newest[e.SessionID]; ok && e.At.Before(held.At) {
			continue
		}
		newest[e.SessionID] = e
	}

	out := make([]session.Event, 0, len(newest))
	for _, e := range newest {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b session.Event) int {
		if a.At.Equal(b.At) {
			return strings.Compare(a.SessionID, b.SessionID)
		}
		return a.At.Compare(b.At)
	})
	return out
}

// compactIfLarge rewrites the log with one event per session once it grows past
// compactAboveBytes.
//
// It writes a temporary file and renames it, so a reader either sees the old
// complete log or the new one. A reader following the file by offset will see it
// shrink, which Follow handles by starting again from the beginning.
func (s *Store) compactIfLarge() error {
	info, err := os.Stat(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reporter: checking the size of %s: %w", s.Path, err)
	}
	if info.Size() <= compactAboveBytes {
		return nil
	}

	events, err := s.readAll()
	if err != nil {
		return err
	}
	kept := latestPerSession(events)

	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".events-*.tmp")
	if err != nil {
		return fmt.Errorf("reporter: creating a temporary file to compact into: %w", err)
	}
	defer os.Remove(tmp.Name())

	writer := bufio.NewWriter(tmp)
	for _, e := range kept {
		line, err := e.Encode()
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := writer.Write(line); err != nil {
			tmp.Close()
			return fmt.Errorf("reporter: writing the compacted log: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		tmp.Close()
		return fmt.Errorf("reporter: flushing the compacted log: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("reporter: setting permissions on the compacted log: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("reporter: closing the compacted log: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return fmt.Errorf("reporter: replacing %s: %w", s.Path, err)
	}
	return nil
}
