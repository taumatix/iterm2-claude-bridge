package bridge_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-claude-bridge/domains/bridge"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// fakeTerminal stands in for iTerm2.
//
// Nothing in the test suite can reach a real iTerm2: the API is off by default
// and has to be switched on by a human. So this records what the bridge asked for
// and answers plausibly, and the README says plainly that the iTerm2 side is
// unverified against the application.
type fakeTerminal struct {
	mu sync.Mutex

	// hierarchy is what ListSessions returns.
	hierarchy *iterm2.Hierarchy
	// variables maps an iTerm2 session id to its variables.
	variables map[string]map[string]string

	createdTab   *iterm2.NewTab
	createOpts   []iterm2.CreateTabOptions
	setVariables [][3]string
	registered   []*apipb.RegisterToolRequest

	// keyWindow and currentWindow are what a FocusRequest reports. Empty means
	// no window of that kind, which is what iTerm2 answers when the application
	// has no terminal window open.
	keyWindow     string
	currentWindow string

	createErr error
	listErr   error
	setErr    error
	focusErr  error
}

func newFakeTerminal() *fakeTerminal {
	return &fakeTerminal{
		hierarchy: &iterm2.Hierarchy{},
		variables: map[string]map[string]string{},
		keyWindow: "w-front",
		createdTab: &iterm2.NewTab{
			WindowID:  "w-new",
			TabID:     "7",
			SessionID: "s-new",
		},
	}
}

func (f *fakeTerminal) ListSessions(context.Context) (*iterm2.Hierarchy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.hierarchy, nil
}

func (f *fakeTerminal) CreateTab(_ context.Context, opts iterm2.CreateTabOptions) (*iterm2.NewTab, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createOpts = append(f.createOpts, opts)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createdTab, nil
}

func (f *fakeTerminal) GetStringVariable(_ context.Context, _ iterm2.VariableScope, id, name string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.variables[id][name]
	return value, ok, nil
}

func (f *fakeTerminal) SetStringVariable(_ context.Context, _ iterm2.VariableScope, id, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.setVariables = append(f.setVariables, [3]string{id, name, value})
	if f.variables[id] == nil {
		f.variables[id] = map[string]string{}
	}
	f.variables[id][name] = value
	return nil
}

func (f *fakeTerminal) Do(_ context.Context, req *apipb.ClientOriginatedMessage) (*apipb.ServerOriginatedMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.GetFocusRequest() != nil {
		if f.focusErr != nil {
			return nil, f.focusErr
		}
		var notifications []*apipb.FocusChangedNotification
		add := func(id string, status apipb.FocusChangedNotification_Window_WindowStatus) {
			notifications = append(notifications, &apipb.FocusChangedNotification{
				Event: &apipb.FocusChangedNotification_Window_{
					Window: &apipb.FocusChangedNotification_Window{
						WindowId:     proto.String(id),
						WindowStatus: status.Enum(),
					},
				},
			})
		}
		// Current first, so a test proving the key window wins is not passing
		// merely because it was seen first.
		if f.currentWindow != "" {
			add(f.currentWindow, apipb.FocusChangedNotification_Window_TERMINAL_WINDOW_IS_CURRENT)
		}
		if f.keyWindow != "" {
			add(f.keyWindow, apipb.FocusChangedNotification_Window_TERMINAL_WINDOW_BECAME_KEY)
		}
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_FocusResponse{
				FocusResponse: &apipb.FocusResponse{Notifications: notifications},
			},
		}, nil
	}
	if tool := req.GetRegisterToolRequest(); tool != nil {
		f.registered = append(f.registered, tool)
		return &apipb.ServerOriginatedMessage{
			Submessage: &apipb.ServerOriginatedMessage_RegisterToolResponse{
				RegisterToolResponse: &apipb.RegisterToolResponse{
					Status: apipb.RegisterToolResponse_OK.Enum(),
				},
			},
		}, nil
	}
	return &apipb.ServerOriginatedMessage{}, nil
}

func (f *fakeTerminal) lastCreateCommand(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.createOpts, "no tab was created")
	return f.createOpts[len(f.createOpts)-1].CustomProfileProperties["Command"]
}

// startPanel brings up a panel over a registry and closes it on cleanup.
func startPanel(t *testing.T, registry *session.Registry, term bridge.Terminal) *bridge.Panel {
	t.Helper()
	opener := &bridge.Opener{Terminal: term, Log: quietLog()}
	p, err := bridge.NewPanel(registry, opener, quietLog())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func get(t *testing.T, rawURL string) (int, string) {
	t.Helper()
	resp, err := http.Get(rawURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func postOpen(t *testing.T, p *bridge.Panel, key, token string) (int, string) {
	t.Helper()
	base, err := url.Parse(p.URL())
	require.NoError(t, err)

	form := url.Values{"key": {key}, "token": {token}}
	resp, err := http.PostForm("http://"+base.Host+"/open", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// remoteSession puts one session in a registry and returns its key.
func remoteSession(t *testing.T, r *session.Registry, host, id string, status session.Status, tmux string) string {
	t.Helper()
	e := session.Event{
		Host:      host,
		SessionID: id,
		Status:    status,
		At:        time.Now().UTC(),
		Cwd:       "/srv/app",
	}
	if tmux != "" {
		e.Tmux = session.TmuxTarget{Session: tmux, Window: "@1"}
	}
	require.True(t, r.Apply(e))
	return e.Key()
}

func TestPanelRendersTheSessionsItIsWatching(t *testing.T) {
	registry := session.NewRegistry()
	remoteSession(t, registry, "build-box", "s1", session.StatusWaiting, "work")
	p := startPanel(t, registry, newFakeTerminal())

	code, body := get(t, p.URL())
	require.Equal(t, http.StatusOK, code)

	assert.Contains(t, body, "build-box")
	assert.Contains(t, body, "/srv/app")
	// Text as well as colour, since these statuses are a common confusion pair.
	assert.Contains(t, body, "needs you")
}

func TestPanelSaysSoWhenThereIsNothingToShow(t *testing.T) {
	p := startPanel(t, session.NewRegistry(), newFakeTerminal())

	code, body := get(t, p.URL())
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "No remote Claude sessions")
}

func TestPanelEscapesValuesThatCameFromAnotherMachine(t *testing.T) {
	// A working directory and a tmux session name are chosen on the remote host, so
	// they are not this program's to trust. The panel runs inside iTerm2's web view.
	registry := session.NewRegistry()
	e := session.Event{
		Host:      "box",
		SessionID: "s1",
		Status:    session.StatusIdle,
		At:        time.Now().UTC(),
		Cwd:       `/srv/<script>alert("x")</script>`,
		Tmux:      session.TmuxTarget{Session: `</script><img src=x onerror=alert(1)>`},
	}
	require.True(t, registry.Apply(e))
	p := startPanel(t, registry, newFakeTerminal())

	_, body := get(t, p.URL())

	assert.NotContains(t, body, "<script>alert", "the cwd was interpolated as markup")
	assert.NotContains(t, body, "<img src=x", "the tmux name was interpolated as markup")
	// Escaped, not dropped: the user still needs to see the path.
	assert.Contains(t, body, "&lt;script&gt;")
}

func TestPanelRefusesARequestWithoutTheToken(t *testing.T) {
	// Loopback stops another machine, not another program on this one — a web page
	// in the user's browser can POST to 127.0.0.1.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")
	p := startPanel(t, registry, newFakeTerminal())

	base, err := url.Parse(p.URL())
	require.NoError(t, err)

	code, _ := get(t, "http://"+base.Host+"/")
	assert.Equal(t, http.StatusForbidden, code, "the index needs the token")

	code, _ = postOpen(t, p, key, "")
	assert.Equal(t, http.StatusForbidden, code, "opening needs the token")

	code, _ = postOpen(t, p, key, "wrong-token")
	assert.Equal(t, http.StatusForbidden, code)
}

func TestPanelListensOnLoopbackOnly(t *testing.T) {
	// What is running where is not for the network to see.
	p := startPanel(t, session.NewRegistry(), newFakeTerminal())
	assert.True(t, strings.HasPrefix(p.URL(), "http://127.0.0.1:"), "URL was %s", p.URL())
}

func TestClickingARowSendsAKeyThePanelStillRecognises(t *testing.T) {
	// The key makes a round trip every other test in this file skips: it is
	// rendered into an HTML attribute, parsed by a browser, and posted back.
	//
	// v0.1.0 joined the two halves of a key with a NUL byte, which html/template
	// replaces with U+FFFD (htmlReplacementTable[0]) and which the HTML5
	// tokenizer would replace anyway. So the key arriving at /open never matched
	// the one the registry held, and every click — on every row, tmux or not —
	// answered "that session is no longer being reported". Posting a key taken
	// straight from the registry cannot see that. Only reading it back out of the
	// rendered page can.
	registry := session.NewRegistry()
	remoteSession(t, registry, "build-box", "5f3a9c2e-1d7b-4a55-9e10-2c4f8b6d0a13", session.StatusWaiting, "work")

	term := newFakeTerminal()
	p := startPanel(t, registry, term)

	code, page := get(t, p.URL())
	require.Equal(t, http.StatusOK, code)

	code, body := postOpen(t, p, dataKeyFromRenderedPanel(t, page), tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"created":true`)
}

func TestAKeySurvivesTheRoundTripWhateverTheHostAndSessionAreCalled(t *testing.T) {
	// Host aliases come from the user's ssh config and session ids from Claude, so
	// neither is this program's to constrain. Whatever they contain has to come
	// back out of an HTML attribute as itself.
	registry := session.NewRegistry()
	e := session.Event{
		Host:      `box "one" & <two>/three`,
		SessionID: "sessioñ id\twith\x00control",
		Status:    session.StatusWaiting,
		At:        time.Now().UTC(),
		Cwd:       "/srv/app",
		Tmux:      session.TmuxTarget{Session: "work", Window: "@1"},
	}
	require.True(t, registry.Apply(e))

	p := startPanel(t, registry, newFakeTerminal())

	code, page := get(t, p.URL())
	require.Equal(t, http.StatusOK, code)

	code, body := postOpen(t, p, dataKeyFromRenderedPanel(t, page), tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)
}

// dataKeyFromRenderedPanel reads the data-key of the first row the way iTerm2's
// web view does.
//
// golang.org/x/net/html implements the HTML5 tokenizer, so it reproduces the
// character-level substitutions a browser makes rather than trusting the bytes
// the template emitted — which is the point: a string match against the template
// output is the encoder checking its own work.
func dataKeyFromRenderedPanel(t *testing.T, page string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)

	var key string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if key != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "li" {
			for _, a := range n.Attr {
				if a.Key == "data-key" {
					key = a.Val
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	require.NotEmpty(t, key, "the panel rendered no row carrying a data-key")
	return key
}

func TestClickingARowOpensATabRunningTheSSHCommand(t *testing.T) {
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "build-box", "s1", session.StatusWaiting, "work")

	term := newFakeTerminal()
	p := startPanel(t, registry, term)
	token := tokenFrom(t, p)

	code, body := postOpen(t, p, key, token)
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"created":true`)

	command := term.lastCreateCommand(t)
	// The command is a JSON string, because that is what api.proto's
	// ProfileProperty.json_value takes.
	assert.True(t, strings.HasPrefix(command, `"`), "command should be JSON: %s", command)
	assert.Contains(t, command, "ssh")
	assert.Contains(t, command, "build-box")
	assert.Contains(t, command, "attach-session")
	assert.Contains(t, command, "work")
}

func TestOpeningATabSetsCustomCommandSoTheProfileRunsIt(t *testing.T) {
	// Read from iTerm2's own Python library: "Custom Command" takes the string
	// "Yes". Without it the profile ignores "Command" and opens a plain shell.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)

	term.mu.Lock()
	defer term.mu.Unlock()
	require.NotEmpty(t, term.createOpts)
	assert.Equal(t, `"Yes"`, term.createOpts[0].CustomProfileProperties["Custom Command"])
}

func TestANewTabIsTaggedSoALaterClickFindsItAgain(t *testing.T) {
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	p := startPanel(t, registry, term)

	code, _ := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code)

	term.mu.Lock()
	defer term.mu.Unlock()
	require.Len(t, term.setVariables, 1)
	assert.Equal(t, "s-new", term.setVariables[0][0])
	assert.Equal(t, bridge.TagVariable, term.setVariables[0][1])
	// The tag names the host and tmux session, not the Claude session: a tab is
	// attached to a tmux session.
	assert.Equal(t, "box/work", term.setVariables[0][2])
}

func TestClickingARowWithATabAlreadyOpenDoesNotOpenAnother(t *testing.T) {
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	// An existing session already tagged for this host and tmux session, as a
	// previous click would have left it.
	term.hierarchy = hierarchyWith("s-existing")
	term.variables["s-existing"] = map[string]string{bridge.TagVariable: "box/work"}

	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"created":false`)

	term.mu.Lock()
	defer term.mu.Unlock()
	assert.Empty(t, term.createOpts, "an existing tab should be activated, not duplicated")
}

func TestANewTabGoesInTheWindowTheUserIsLookingAt(t *testing.T) {
	// A click arrives from the toolbelt of the window in front, so that is where
	// the tab belongs. CreateTab with no window id opens a whole new window,
	// which is what v0.1.1 did on every click.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	term.keyWindow = "w-front"
	term.currentWindow = "w-behind"
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)

	term.mu.Lock()
	defer term.mu.Unlock()
	require.Len(t, term.createOpts, 1)
	assert.Equal(t, "w-front", term.createOpts[0].WindowID)
}

func TestANewTabFallsBackToTheCurrentWindowWhenNoneIsKey(t *testing.T) {
	// Something other than a terminal window has focus — iTerm2 then reports a
	// window as current but not key, and that is still where the user is working.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	term.keyWindow = ""
	term.currentWindow = "w-behind"
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)

	term.mu.Lock()
	defer term.mu.Unlock()
	require.Len(t, term.createOpts, 1)
	assert.Equal(t, "w-behind", term.createOpts[0].WindowID)
}

func TestAClickStillOpensATabWhenIStermCannotSayWhichWindowIsInFront(t *testing.T) {
	// A tab in the wrong place beats a click that does nothing.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	term.focusErr = assert.AnError
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)

	term.mu.Lock()
	defer term.mu.Unlock()
	require.Len(t, term.createOpts, 1)
	assert.Empty(t, term.createOpts[0].WindowID, "an empty window id is what makes iTerm2 open a new window")
}

func TestATabRunsSSHThroughTheUsersLoginShell(t *testing.T) {
	// Without this iTerm2 exec's ssh directly, so no dotfile runs and a
	// SSH_AUTH_SOCK set in ~/.zshrc — the agent holding the user's keys — is
	// missing. The key and its default (NO) are iTerm2's own, from
	// ITAddressBookMgr.h KEY_RUN_COMMAND_IN_LOGIN_SHELL.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)

	term.mu.Lock()
	defer term.mu.Unlock()
	require.Len(t, term.createOpts, 1)
	props := term.createOpts[0].CustomProfileProperties
	// JSON, matching ProfileProperty.json_value: a bare boolean, not a quoted one.
	assert.Equal(t, "true", props["Run Command In Login Shell"])
	assert.Equal(t, `"Yes"`, props["Custom Command"])
}

func TestAClickIsRefusedWhenTheCommandWouldBecomeAnITerm2Expression(t *testing.T) {
	// iTerm2 evaluates a profile's Command as an interpolated string before
	// splitting it into arguments, and its parser starts an expression at `\(`.
	// A tmux session name is chosen on the remote host, so shell quoting — which
	// happens a layer later — does not protect this.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, `work\(iterm2.run("say pwned"))`)

	term := newFakeTerminal()
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, body, "evaluates as an expression")

	term.mu.Lock()
	defer term.mu.Unlock()
	assert.Empty(t, term.createOpts, "no tab should be opened for a command iTerm2 would evaluate")
}

func TestAnOrdinaryQuoteInATmuxNameIsStillAttachable(t *testing.T) {
	// The refusal above must be the expression opener specifically. Shell quoting
	// emits a backslash for an embedded single quote ('\''), and that is harmless
	// — iTerm2's parser only starts an expression at a backslash followed by "(".
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "it's work")

	term := newFakeTerminal()
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)

	// That it is attachable is the claim here. Whether the name survives the two
	// shells between this and tmux byte for byte is settled in quoting_test.go,
	// by real shells, rather than by matching a quoted string here — the shape of
	// that string changed the moment a second layer of quoting was added, and an
	// assertion that has to be rewritten when the encoding changes was never
	// testing the behaviour.
	var command string
	require.NoError(t, json.Unmarshal([]byte(term.lastCreateCommand(t)), &command))
	assert.Contains(t, command, "attach-session")
}

func TestTagsStayDistinctWhenAHostOrTmuxNameContainsASlash(t *testing.T) {
	// A tab is found by its tag, so two different tabs sharing one would make a
	// click reveal whichever iTerm2 listed first.
	assert.NotEqual(t,
		bridge.Tag("a", session.TmuxTarget{Session: "b/c"}),
		bridge.Tag("a/b", session.TmuxTarget{Session: "c"}))

	// Ordinary names are unchanged, so a tab tagged by an earlier version is
	// still recognised.
	assert.Equal(t, "box/work", bridge.Tag("box", session.TmuxTarget{Session: "work"}))
}

func TestClickingARowTaggedForAnotherSessionOpensItsOwnTab(t *testing.T) {
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	term.hierarchy = hierarchyWith("s-other")
	term.variables["s-other"] = map[string]string{bridge.TagVariable: "box/something-else"}

	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"created":true`)
}

func TestClickingASessionThatIsNoLongerReportedSaysSo(t *testing.T) {
	registry := session.NewRegistry()
	p := startPanel(t, registry, newFakeTerminal())

	code, body := postOpen(t, p, session.Key("box", "gone"), tokenFrom(t, p))
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, body, "no longer being reported")
}

func TestClickingASessionWithNoTmuxSessionIsRefused(t *testing.T) {
	// Claude running outside tmux has nowhere for a tab to go.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "")

	term := newFakeTerminal()
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, body, "no tmux session")

	term.mu.Lock()
	defer term.mu.Unlock()
	assert.Empty(t, term.createOpts)
}

func TestAFailureToTagIsReportedRatherThanSwallowed(t *testing.T) {
	// A tab this program cannot recognise later means the next click opens another.
	registry := session.NewRegistry()
	key := remoteSession(t, registry, "box", "s1", session.StatusIdle, "work")

	term := newFakeTerminal()
	term.setErr = assert.AnError
	p := startPanel(t, registry, term)

	code, body := postOpen(t, p, key, tokenFrom(t, p))
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.Contains(t, body, "could not be tagged")
}

func TestRegisterPanelAsksForAWebViewTool(t *testing.T) {
	term := newFakeTerminal()
	require.NoError(t, bridge.RegisterPanel(context.Background(), term, "http://127.0.0.1:1234/?token=x", true))

	term.mu.Lock()
	defer term.mu.Unlock()
	require.Len(t, term.registered, 1)
	got := term.registered[0]
	assert.Equal(t, bridge.ToolName, got.GetName())
	// api.proto asks for a bundle-style identifier so two tools cannot collide.
	assert.Equal(t, bridge.ToolIdentifier, got.GetIdentifier())
	assert.Equal(t, apipb.RegisterToolRequest_WEB_VIEW_TOOL, got.GetToolType())
	assert.Equal(t, "http://127.0.0.1:1234/?token=x", got.GetURL())
	assert.True(t, got.GetRevealIfAlreadyRegistered())
}

// hierarchyWith builds a hierarchy holding one session with the given id.
func hierarchyWith(sessionID string) *iterm2.Hierarchy {
	return &iterm2.Hierarchy{
		BuriedSessions: nil,
		Windows: []*iterm2.Window{{
			ID: "w-1",
			Tabs: []*iterm2.Tab{{
				ID:       "t-1",
				WindowID: "w-1",
				Sessions: []*iterm2.Session{{ID: sessionID}},
			}},
		}},
	}
}

// tokenFrom pulls the token out of the panel's URL, which is how iTerm2 receives
// it.
func tokenFrom(t *testing.T, p *bridge.Panel) string {
	t.Helper()
	parsed, err := url.Parse(p.URL())
	require.NoError(t, err)
	token := parsed.Query().Get("token")
	require.NotEmpty(t, token)
	return token
}

// sessionLastHeardFrom records a session whose last event was ago in the past.
func sessionLastHeardFrom(t *testing.T, r *session.Registry, host string, status session.Status, ago time.Duration) {
	t.Helper()
	require.True(t, r.Apply(session.Event{
		Host:      host,
		SessionID: host + "-s",
		Status:    status,
		At:        time.Now().UTC().Add(-ago),
		Cwd:       "/srv/app",
	}))
}

// A working session reports on every tool call, so one that has said nothing
// for longer than the threshold has most likely lost its hook — a crashed CLI,
// a killed shell — and its "working" is no longer news.
func TestPanelMarksAWorkingSessionThatHasGoneQuietAsStale(t *testing.T) {
	registry := session.NewRegistry()
	sessionLastHeardFrom(t, registry, "quiet-box", session.StatusWorking, 20*time.Minute)
	sessionLastHeardFrom(t, registry, "busy-box", session.StatusWorking, time.Second)
	p := startPanel(t, registry, newFakeTerminal())

	_, body := get(t, p.URL())

	quiet, busy := rowFor(t, body, "quiet-box"), rowFor(t, body, "busy-box")
	assert.Contains(t, quiet, `class="stale"`)
	assert.Contains(t, quiet, "no update for 20m")
	assert.NotContains(t, busy, "stale")
	assert.NotContains(t, busy, "no update")
}

// Waiting and idle are states a session rests in: hours without an event is
// what they look like when everything is fine.
func TestPanelDoesNotMarkARestingSessionStale(t *testing.T) {
	registry := session.NewRegistry()
	sessionLastHeardFrom(t, registry, "waiting-box", session.StatusWaiting, 3*time.Hour)
	sessionLastHeardFrom(t, registry, "idle-box", session.StatusIdle, 3*time.Hour)
	p := startPanel(t, registry, newFakeTerminal())

	_, body := get(t, p.URL())

	for _, host := range []string{"waiting-box", "idle-box"} {
		assert.NotContains(t, rowFor(t, body, host), "stale", host)
	}
}

func TestPanelStaleThresholdIsConfigurableAndCanBeTurnedOff(t *testing.T) {
	registry := session.NewRegistry()
	sessionLastHeardFrom(t, registry, "quiet-box", session.StatusWorking, 2*time.Minute)
	p := startPanel(t, registry, newFakeTerminal())

	_, body := get(t, p.URL())
	assert.NotContains(t, rowFor(t, body, "quiet-box"), "stale", "under the default threshold")

	p.SetStaleAfter(time.Minute)
	_, body = get(t, p.URL())
	assert.Contains(t, rowFor(t, body, "quiet-box"), `class="stale"`)

	p.SetStaleAfter(0)
	_, body = get(t, p.URL())
	assert.NotContains(t, rowFor(t, body, "quiet-box"), "stale", "0 turns it off")
}

// rowFor returns the <li> element that shows host.
func rowFor(t *testing.T, body, host string) string {
	t.Helper()
	i := strings.Index(body, ">"+host+"<")
	require.NotEqual(t, -1, i, "no row for %s", host)
	start := strings.LastIndex(body[:i], "<li")
	end := strings.Index(body[i:], "</li>")
	require.True(t, start >= 0 && end >= 0)
	return body[start : i+end]
}

// A host whose stream sends no hello runs a build from before protocol 2, so it
// cannot report a session whose Claude exited. The panel says so, for that host
// only, through the real watcher and the real page.
func TestPanelNamesAHostRunningAnOlderBuild(t *testing.T) {
	registry := session.NewRegistry()
	event := eventLine(t, "s1", session.StatusWaiting, "work")
	hello, err := session.Hello{Protocol: session.Protocol, Version: "v9.9.9", CheckProcesses: true}.Encode()
	require.NoError(t, err)
	current := emit(t, string(hello), event)
	old := emit(t, event)
	launcher := &scriptLauncher{script: `if [ "$BRIDGE_TEST_HOST" = old-box ]; then ` + old + `; else ` + current + `; fi` + holdOpen}
	w := &bridge.Watcher{Registry: registry, Launcher: launcher, Log: quietLog()}
	watchInBackground(t, w, "old-box", "new-box")
	waitFor(t, "both hosts to report", func() bool { return len(registry.Sessions()) == 2 })

	p := startPanel(t, registry, newFakeTerminal())
	_, body := get(t, p.URL())

	assert.Contains(t, body, `data-outdated-host="old-box"`)
	assert.Contains(t, body, "old-box runs an older iterm2-claude-bridge")
	assert.NotContains(t, body, `data-outdated-host="new-box"`, "a current host was called outdated")
	assert.Equal(t, session.Protocol, registry.Remotes()["new-box"].Protocol)
	assert.Equal(t, "v9.9.9", registry.Remotes()["new-box"].Version)
}

// The notice goes with the stream: a host no longer connected is not named.
func TestPanelForgetsAnOutdatedHostWhenItsStreamEnds(t *testing.T) {
	registry := session.NewRegistry()
	registry.SetRemote("old-box", session.Hello{Protocol: 1})
	registry.ForgetHost("old-box")
	p := startPanel(t, registry, newFakeTerminal())
	_, body := get(t, p.URL())
	assert.NotContains(t, body, "old-box")
}

// A working session whose host checks its Claude process is reported gone if
// that process exits, so a long quiet turn there is a turn, not a lost hook:
// marking it stale would be the one false alarm v0.3.0 left. A session the
// host cannot check keeps the marking.
func TestPanelDoesNotMarkStaleASessionItsHostIsCheckingTheProcessOf(t *testing.T) {
	registry := session.NewRegistry()
	quietWorking := func(host, id string, p *session.Process) {
		require.True(t, registry.Apply(session.Event{
			Host: host, SessionID: id, Status: session.StatusWorking,
			At: time.Now().UTC().Add(-20 * time.Minute), Cwd: "/srv/app", Process: p,
		}))
	}
	proc := &session.Process{PID: 4242, Started: "Mon Oct 5 00:00:00 2026"}
	registry.SetRemote("checked-box", session.Hello{Protocol: session.Protocol, CheckProcesses: true})
	registry.SetRemote("unchecked-box", session.Hello{Protocol: session.Protocol, CheckProcesses: false})
	quietWorking("checked-box", "with-process", proc)
	quietWorking("unchecked-box", "with-process", proc)
	registry.SetRemote("noproc-box", session.Hello{Protocol: session.Protocol, CheckProcesses: true})
	quietWorking("noproc-box", "without-process", nil)

	p := startPanel(t, registry, newFakeTerminal())
	_, body := get(t, p.URL())

	assert.NotContains(t, rowFor(t, body, "checked-box"), "stale", "a checked session was marked stale")
	assert.Contains(t, rowFor(t, body, "unchecked-box"), `class="stale"`, "the host does not check, so the marking stays")
	assert.Contains(t, rowFor(t, body, "noproc-box"), `class="stale"`, "no process recorded, so nothing is checked")
}

// A later event without a process (a hook that could not tell) keeps the one an
// earlier event recorded.
func TestALaterEventWithoutAProcessKeepsTheRecordedOne(t *testing.T) {
	registry := session.NewRegistry()
	proc := &session.Process{PID: 7, Started: "x"}
	base := session.Event{Host: "h", SessionID: "s", Status: session.StatusWorking, At: time.Now().UTC().Add(-time.Minute), Process: proc}
	require.True(t, registry.Apply(base))
	later := base
	later.At, later.Process, later.Status = time.Now().UTC(), nil, session.StatusWaiting
	registry.Apply(later)

	got, ok := registry.Lookup(base.Key())
	require.True(t, ok)
	require.NotNil(t, got.Process)
	assert.Equal(t, 7, got.Process.PID)
}
