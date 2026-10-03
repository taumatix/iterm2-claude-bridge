package bridge_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-go/apipb"
)

// itermServer speaks enough of iTerm2's API over a real unix socket for the
// bridge's connection handling to be driven end to end: the real
// iterm2.Connect, the real WebSocket handshake, real protobuf frames. It answers
// RegisterToolRequest and ListSessionsRequest, records what it was asked, and
// can drop every connection and come back on the same path, which is what an
// iTerm2 restart looks like from the client.
//
// It is a second, smaller copy of the fake iterm2-go keeps in an internal
// package, which another module cannot import.
type itermServer struct {
	t    *testing.T
	path string

	mu         sync.Mutex
	srv        *http.Server
	conns      []*websocket.Conn
	handshakes int
	registered []string // RegisterToolRequest URLs, in order
	listed     int      // ListSessionsRequests answered
}

func startITermServer(t *testing.T) *itermServer {
	t.Helper()
	// Short: sun_path is 104 bytes on Darwin and t.TempDir() spends most of it.
	dir, err := os.MkdirTemp("", "itb")
	require.NoError(t, err)
	s := &itermServer{t: t, path: filepath.Join(dir, "s")}
	t.Cleanup(func() {
		s.stop()
		_ = os.RemoveAll(dir)
	})
	s.start()
	return s
}

func (s *itermServer) start() {
	s.t.Helper()
	ln, err := net.Listen("unix", s.path)
	require.NoError(s.t, err)
	srv := &http.Server{Handler: http.HandlerFunc(s.serve)}
	s.mu.Lock()
	s.srv = srv
	s.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
}

// stop closes the listener and every connection, as iTerm2 quitting does.
func (s *itermServer) stop() {
	s.mu.Lock()
	srv, conns := s.srv, s.conns
	s.srv, s.conns = nil, nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.CloseNow()
	}
	if srv != nil {
		_ = srv.Close()
	}
	_ = os.Remove(s.path)
}

// restart is iTerm2 quitting and coming back after gap.
func (s *itermServer) restart(gap time.Duration) {
	s.stop()
	time.Sleep(gap)
	s.start()
}

func (s *itermServer) serve(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"api.iterm2.com"}})
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 20)
	s.mu.Lock()
	s.handshakes++
	s.conns = append(s.conns, c)
	s.mu.Unlock()

	for {
		typ, payload, err := c.Read(context.Background())
		if err != nil || typ != websocket.MessageBinary {
			return
		}
		var req apipb.ClientOriginatedMessage
		if proto.Unmarshal(payload, &req) != nil {
			return
		}
		resp := &apipb.ServerOriginatedMessage{Id: req.Id}
		switch {
		case req.GetRegisterToolRequest() != nil:
			s.mu.Lock()
			s.registered = append(s.registered, req.GetRegisterToolRequest().GetURL())
			s.mu.Unlock()
			resp.Submessage = &apipb.ServerOriginatedMessage_RegisterToolResponse{
				RegisterToolResponse: &apipb.RegisterToolResponse{Status: apipb.RegisterToolResponse_OK.Enum()},
			}
		case req.GetListSessionsRequest() != nil:
			s.mu.Lock()
			s.listed++
			s.mu.Unlock()
			resp.Submessage = &apipb.ServerOriginatedMessage_ListSessionsResponse{
				ListSessionsResponse: &apipb.ListSessionsResponse{},
			}
		}
		out, err := proto.Marshal(resp)
		if err != nil || c.Write(context.Background(), websocket.MessageBinary, out) != nil {
			return
		}
	}
}

func (s *itermServer) snapshot() (handshakes int, registered []string, listed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handshakes, append([]string(nil), s.registered...), s.listed
}

// eventually waits for cond, failing with what the server saw if it never holds.
func (s *itermServer) eventually(cond func(handshakes int, registered []string, listed int) bool, msg string) {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond(s.snapshot()) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h, r, l := s.snapshot()
	s.t.Fatalf("%s (handshakes=%d registered=%v listed=%d)", msg, h, r, l)
}
