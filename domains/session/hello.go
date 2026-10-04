package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Protocol is the level of the stream this build writes and expects.
//
//  1. Events only. Every release before v0.5.0, which sent no [Hello].
//  2. A [Hello] first; events may carry a [Process], and a stream that says
//     CheckProcesses reports a session gone when that process exits.
//
// It rises only when a watcher would show less than it should without what
// the new level adds, so that a remote below it is worth telling the user
// about.
const Protocol = 2

// Hello is the first line a stream writes, saying what is at the other end.
//
// An older watcher does not know it: the line has no session_id, so it is
// logged as unreadable once per connection and skipped, and the events after
// it are read as before.
type Hello struct {
	Protocol int `json:"protocol"`

	// Version is the remote build's version, for a human to read. "dev" for a
	// build without one.
	Version string `json:"version,omitempty"`

	// CheckProcesses says the stream reports a session gone when its Claude
	// process exits.
	CheckProcesses bool `json:"check_processes,omitempty"`
}

type helloLine struct {
	Hello *Hello `json:"hello"`
}

// Encode renders the hello as one line, newline included.
func (h Hello) Encode() ([]byte, error) {
	body, err := json.Marshal(helloLine{Hello: &h})
	if err != nil {
		return nil, fmt.Errorf("session: encoding hello: %w", err)
	}
	return append(body, '\n'), nil
}

// DecodeHello reports whether line is a stream's hello, and what it says.
func DecodeHello(line []byte) (Hello, bool) {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, `{"hello"`) {
		return Hello{}, false
	}
	var l helloLine
	if err := json.Unmarshal([]byte(trimmed), &l); err != nil || l.Hello == nil {
		return Hello{}, false
	}
	return *l.Hello, true
}
