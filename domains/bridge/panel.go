package bridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

//go:embed panel.html
var panelFiles embed.FS

// panelTemplate renders the rows. html/template escapes every interpolation, so
// a working directory or tmux session name containing markup cannot become
// markup — those strings come from another machine and are not this program's to
// trust.
var panelTemplate = template.Must(template.ParseFS(panelFiles, "panel.html"))

// Panel serves the toolbelt web view and handles clicks on it.
type Panel struct {
	Registry *session.Registry
	Opener   *Opener
	Log      *slog.Logger

	// token authorises requests. The panel listens on loopback, which stops
	// another machine reaching it but not another program on this one — including
	// any web page the user visits, since a browser will happily POST to
	// 127.0.0.1. The token is given to iTerm2 in the tool's URL and required on
	// every request.
	token string

	// staleAfter is how long a working session may go without an event before
	// its row says so; 0 turns the marking off. Atomic because the panel is
	// already serving when SetStaleAfter is called.
	staleAfter atomic.Int64

	listener net.Listener
	server   *http.Server
}

// NewPanel starts a panel on a loopback port the operating system chooses.
//
// Choosing the port rather than fixing one means two bridges, or a bridge and
// something else, cannot collide.
func NewPanel(registry *session.Registry, opener *Opener, log *slog.Logger) (*Panel, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}

	// Loopback only: this serves what is running where, which is not for the
	// network to see.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bridge: listening on loopback: %w", err)
	}

	p := &Panel{
		Registry: registry,
		Opener:   opener,
		Log:      log,
		token:    token,
		listener: listener,
	}
	p.staleAfter.Store(int64(DefaultStaleAfter))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", p.handleIndex)
	mux.HandleFunc("POST /open", p.handleOpen)

	p.server = &http.Server{
		Handler: mux,
		// A click leads to an ssh connection and an iTerm2 round trip, so the write
		// timeout is generous; the read one is not, since requests are tiny.
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	go func() {
		if err := p.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			p.log().Error("panel stopped serving", "error", err)
		}
	}()
	return p, nil
}

// DefaultStaleAfter is how long a working session may go without an event
// before the panel marks it stale. A working session reports on every tool
// call, and ten minutes is longer than any single tool call or model turn
// usually takes; one that has said nothing for that long has most likely lost
// its hook, through a crashed CLI or a killed shell.
const DefaultStaleAfter = 10 * time.Minute

// SetStaleAfter changes how long a working session may go without an event
// before its row is marked stale. 0 turns the marking off. Only working
// sessions are ever marked: waiting and idle are states a session rests in.
func (p *Panel) SetStaleAfter(d time.Duration) {
	p.staleAfter.Store(int64(d))
}

// URL is the address to register with iTerm2, carrying the token.
func (p *Panel) URL() string {
	return fmt.Sprintf("http://%s/?token=%s", p.listener.Addr().String(), p.token)
}

// Close stops serving.
func (p *Panel) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.server.Shutdown(ctx)
}

// row is one line of the panel.
type row struct {
	Key        string
	Host       string
	Cwd        string
	Status     string
	StatusText string
	Tmux       string
	Attachable bool
	Age        string
	// Stale is set when a working session has gone quiet for longer than the
	// threshold, so its status is a last known one rather than a current one.
	Stale bool
}

// handleIndex renders the current state.
func (p *Panel) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !p.authorised(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	sessions := p.Registry.Sessions()
	rows := make([]row, 0, len(sessions))
	staleAfter := time.Duration(p.staleAfter.Load())
	for _, s := range sessions {
		quiet := time.Since(s.Since)
		rows = append(rows, row{
			Key:        s.Key(),
			Host:       s.Host,
			Cwd:        s.Cwd,
			Status:     string(s.Status),
			StatusText: statusText(s.Status),
			Tmux:       s.Tmux.Session,
			Attachable: s.Attachable(),
			Age:        humaniseAge(quiet),
			Stale:      staleAfter > 0 && s.Status == session.StatusWorking && quiet > staleAfter,
		})
	}

	data := struct {
		Token string
		Rows  []row
	}{Token: p.token, Rows: rows}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The panel re-reads itself; nothing here should be cached.
	w.Header().Set("Cache-Control", "no-store")
	if err := panelTemplate.Execute(w, data); err != nil {
		p.log().Error("rendering the panel", "error", err)
	}
}

// handleOpen reveals the tab for the clicked row.
func (p *Panel) handleOpen(w http.ResponseWriter, r *http.Request) {
	if !p.authorised(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	key := r.FormValue("key")
	s, ok := p.Registry.Lookup(key)
	if !ok {
		// The session ended between the render and the click.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "that session is no longer being reported"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	sessionID, created, err := p.Opener.Reveal(ctx, s)
	if err != nil {
		p.log().Error("could not reveal a session", "host", s.Host, "session", s.SessionID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p.log().Info("revealed a remote session",
		"host", s.Host, "tmux", s.Tmux.Session, "iterm_session", sessionID, "created_tab", created)

	writeJSON(w, http.StatusOK, map[string]any{"session": sessionID, "created": created})
}

// authorised checks the token, which may arrive as a query parameter — iTerm2
// loads the tool's URL and follows links within it — or as a header.
//
// Compared in constant time out of habit rather than necessity: a timing oracle
// on a loopback socket is not the realistic attack, a web page in the user's
// browser POSTing to 127.0.0.1 is, and any comparison stops that.
func (p *Panel) authorised(r *http.Request) bool {
	presented := r.URL.Query().Get("token")
	if presented == "" {
		presented = r.Header.Get("X-Bridge-Token")
	}
	if presented == "" {
		presented = r.FormValue("token")
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(p.token)) == 1
}

func (p *Panel) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// newToken returns 32 hex characters of randomness.
func newToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("bridge: generating a panel token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// statusText is the label shown next to the colour, so the panel is readable
// without relying on colour alone.
func statusText(s session.Status) string {
	switch s {
	case session.StatusWorking:
		return "working"
	case session.StatusWaiting:
		return "needs you"
	case session.StatusIdle:
		return "idle"
	case session.StatusGone:
		return "ended"
	default:
		// A status from a newer remote build: shown as itself rather than hidden.
		return string(s)
	}
}

// humaniseAge renders a duration the way a glance wants it.
func humaniseAge(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
