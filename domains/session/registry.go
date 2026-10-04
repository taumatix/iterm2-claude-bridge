package session

import (
	"cmp"
	"slices"
	"sync"
	"time"
)

// Session is the current state of one Claude Code session, as the panel shows
// it.
type Session struct {
	Host      string
	SessionID string
	Status    Status
	Since     time.Time
	Tmux      TmuxTarget
	Cwd       string
	HookEvent string
}

// Key identifies the session, matching [Event.Key].
func (s Session) Key() string {
	return Key(s.Host, s.SessionID)
}

// Attachable reports whether there is a tmux session to attach to. A Claude
// session not running under tmux has nowhere for a new tab to go.
func (s Session) Attachable() bool {
	return !s.Tmux.Zero() && s.Status != StatusGone
}

// Registry holds what is currently known about every watched session.
//
// It is safe for concurrent use: events arrive on one goroutine per host while
// the panel reads it to render, and a click reads it to decide what to open.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]Session

	// remotes is what each host's stream said about itself, for as long as the
	// stream lasts.
	remotes map[string]Hello

	// changed is closed and replaced whenever the contents change, so a reader can
	// wait for the next change without polling. Broadcasting by closing a channel
	// rather than with sync.Cond lets a waiter also select on a context.
	changed chan struct{}
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		sessions: make(map[string]Session),
		remotes:  make(map[string]Hello),
		changed:  make(chan struct{}),
	}
}

// Apply folds one event into the registry and reports whether anything a
// watcher would see actually changed.
//
// An event older than what is already held is ignored: the remote half replays
// its recent history when a stream reconnects, so out-of-order and duplicate
// events are normal rather than exceptional.
func (r *Registry) Apply(e Event) (changed bool) {
	if err := e.Validate(); err != nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := e.Key()
	existing, known := r.sessions[key]
	if known && e.At.Before(existing.Since) {
		return false
	}

	next := Session{
		Host:      e.Host,
		SessionID: e.SessionID,
		Status:    e.Status,
		Since:     e.At,
		Tmux:      e.Tmux,
		Cwd:       e.Cwd,
		HookEvent: e.HookEvent,
	}
	// A later event may carry no tmux target or cwd — not every hook payload has
	// one to hand — and dropping what an earlier event established would make the
	// row stop being attachable for no reason.
	if next.Tmux.Zero() {
		next.Tmux = existing.Tmux
	}
	// The same argument one field down: an event from a build predating
	// TmuxTarget.Binary carries no path, and letting it erase one an earlier
	// event established would put the row back to guessing "tmux" on a host whose
	// non-interactive PATH cannot find it. An event log can hold both, because
	// the remote is upgraded under a stream that replays what is already there.
	if next.Tmux.Binary == "" {
		next.Tmux.Binary = existing.Tmux.Binary
	}
	if next.Cwd == "" {
		next.Cwd = existing.Cwd
	}

	if known && sameToAWatcher(existing, next) {
		// Still recorded, so Since advances and a later event is not mistaken for
		// an older one, but no waiter is woken.
		r.sessions[key] = next
		return false
	}

	r.sessions[key] = next
	r.broadcastLocked()
	return true
}

// sameToAWatcher reports whether two states would render identically. The
// timestamp and the hook event that produced them are diagnostic, not visible,
// so a run of PreToolUse events does not wake the panel forty times.
func sameToAWatcher(a, b Session) bool {
	return a.Status == b.Status && a.Tmux == b.Tmux && a.Cwd == b.Cwd
}

// Forget drops a session. It is how a gone session leaves the panel once the
// row has been shown, and how a host being dropped takes its sessions with it.
func (r *Registry) Forget(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[key]; !ok {
		return
	}
	delete(r.sessions, key)
	r.broadcastLocked()
}

// ForgetHost drops every session belonging to host, which is what to do when
// that host's stream ends: its sessions may still be running, but nothing here
// knows their state any more, and showing a stale status is worse than showing
// none.
func (r *Registry) ForgetHost(host string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, removed := r.remotes[host]
	delete(r.remotes, host)
	for key, s := range r.sessions {
		if s.Host == host {
			delete(r.sessions, key)
			removed = true
		}
	}
	if removed {
		r.broadcastLocked()
	}
}

// SetRemote records what host's stream said about itself. A stream from before
// [Protocol] 2 says nothing; the watcher records it as protocol 1.
func (r *Registry) SetRemote(host string, h Hello) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.remotes[host]; ok && old == h {
		return
	}
	r.remotes[host] = h
	r.broadcastLocked()
}

// Remotes returns what each connected host's stream said about itself.
func (r *Registry) Remotes() map[string]Hello {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Hello, len(r.remotes))
	for host, h := range r.remotes {
		out[host] = h
	}
	return out
}

// Sessions returns every session, ordered for display: the ones wanting
// attention first, then by host, then by working directory, so a row does not
// move about between renders for no reason.
func (r *Registry) Sessions() []Session {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Session) int {
		if c := cmp.Compare(statusRank(a.Status), statusRank(b.Status)); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Host, b.Host); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Cwd, b.Cwd); c != 0 {
			return c
		}
		return cmp.Compare(a.SessionID, b.SessionID)
	})
	return out
}

// Lookup returns the session with this key.
func (r *Registry) Lookup(key string) (Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[key]
	return s, ok
}

// Changed returns a channel closed the next time the registry changes. Take it
// before reading, or a change between the read and the wait is missed.
func (r *Registry) Changed() <-chan struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.changed
}

// broadcastLocked wakes everything waiting on Changed. The caller holds the
// write lock.
func (r *Registry) broadcastLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// statusRank orders statuses by how much they want a human's attention.
func statusRank(s Status) int {
	if i := slices.Index(Statuses(), s); i >= 0 {
		return i
	}
	// A status from a newer remote build sorts last rather than first: it should
	// be visible without displacing a session that is known to need attention.
	return len(Statuses())
}
