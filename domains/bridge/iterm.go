package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	iterm2 "github.com/taumatix/iterm2-go"
	"github.com/taumatix/iterm2-go/apipb"
	"google.golang.org/protobuf/proto"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// TagVariable is the iTerm2 session variable this program writes on a tab it
// created, so it can recognise its own tabs later.
//
// iTerm2 requires a variable a script sets to begin with "user.", and the value
// is a tag identifying which remote session the tab is attached to. Recognising
// a tab by a variable rather than by remembering what was created means a
// restarted bridge, or a second one, still finds the existing tab instead of
// opening a duplicate.
const TagVariable = "user.iterm2ClaudeBridge"

// ToolIdentifier is the toolbelt tool's id. api.proto asks for it to be prefixed
// with an app bundle so two tools cannot collide.
const ToolIdentifier = "com.taumatix.iterm2-claude-bridge"

// ToolName is what iTerm2 shows in the toolbelt menu.
const ToolName = "Remote Claude"

// Terminal is the part of iTerm2 this package needs. It is an interface so the
// panel and click handling can be tested without a running iTerm2, which is also
// the only way they can be tested at all on a machine with the API switched off.
type Terminal interface {
	ListSessions(ctx context.Context) (*iterm2.Hierarchy, error)
	CreateTab(ctx context.Context, opts iterm2.CreateTabOptions) (*iterm2.NewTab, error)
	GetStringVariable(ctx context.Context, scope iterm2.VariableScope, identifier, name string) (string, bool, error)
	SetStringVariable(ctx context.Context, scope iterm2.VariableScope, identifier, name, value string) error
	Do(ctx context.Context, req *apipb.ClientOriginatedMessage) (*apipb.ServerOriginatedMessage, error)
}

// Ensure the real client satisfies it.
var _ Terminal = (*iterm2.Conn)(nil)

// Opener finds or creates the iTerm2 tab for a remote session.
type Opener struct {
	Terminal Terminal
	SSH      SSHOptions

	// Profile names the iTerm2 profile new tabs are based on. Empty uses the
	// default profile.
	Profile string
}

// Tag is the value written to [TagVariable] for a session. It identifies the
// host and tmux session rather than the Claude session, because a tab is attached
// to a tmux session: two Claude sessions in one tmux session share the tab.
// It is built with the same encoding as a session key, for the same reason: the
// two halves must not be able to run together. Host "a" with tmux session "b/c"
// and host "a/b" with tmux session "c" are different tabs, and a tag of "a/b/c"
// for both would activate whichever was found first.
func Tag(host string, target session.TmuxTarget) string {
	return session.Key(host, target.Session)
}

// Reveal brings up the tab for s, creating it if there is none.
//
// It returns the iTerm2 session id of the tab now showing it, and whether that
// tab had to be created.
func (o *Opener) Reveal(ctx context.Context, s session.Session) (sessionID string, created bool, err error) {
	if !s.Attachable() {
		return "", false, fmt.Errorf("bridge: %s on %s has no tmux session to attach to", s.SessionID, s.Host)
	}
	tag := Tag(s.Host, s.Tmux)

	existing, err := o.findTagged(ctx, tag)
	if err != nil {
		return "", false, err
	}
	if existing != "" {
		if err := activateSession(ctx, o.Terminal, existing); err != nil {
			return "", false, fmt.Errorf("bridge: activating the existing tab for %s: %w", tag, err)
		}
		return existing, false, nil
	}

	return o.openTab(ctx, s, tag)
}

// activateSession brings one iTerm2 session to the front.
//
// It builds the request rather than calling iterm2.Session.Activate, because that
// method sends through the connection the session was parsed from — which puts
// this one operation outside the Terminal seam every other call goes through, and
// makes it untestable without a real iTerm2. iterm2-go has no
// Conn.ActivateSession(id) convenience yet; when it does, this becomes a call to
// it.
//
// Selecting the pane as well as the tab, and raising the window and the
// application, so a click lands the user in front of it rather than merely
// somewhere nearby.
func activateSession(ctx context.Context, term Terminal, sessionID string) error {
	resp, err := term.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_ActivateRequest{
			ActivateRequest: &apipb.ActivateRequest{
				Identifier:       &apipb.ActivateRequest_SessionId{SessionId: sessionID},
				SelectTab:        proto.Bool(true),
				SelectSession:    proto.Bool(true),
				OrderWindowFront: proto.Bool(true),
				ActivateApp:      &apipb.ActivateRequest_App{},
			},
		},
	})
	if err != nil {
		return err
	}
	if status := resp.GetActivateResponse().GetStatus(); status != apipb.ActivateResponse_OK {
		return fmt.Errorf("iTerm2 declined to activate %s: %s", sessionID, status)
	}
	return nil
}

// findTagged returns the id of the iTerm2 session this program tagged with tag,
// or an empty string when there is none.
//
// It reads the variable on every session, which is a round trip each. That is
// affordable because it happens on a click rather than on every render, and
// because iTerm2 offers no way to ask "which session has this variable".
func (o *Opener) findTagged(ctx context.Context, tag string) (string, error) {
	h, err := o.Terminal.ListSessions(ctx)
	if err != nil {
		return "", fmt.Errorf("bridge: listing iTerm2 sessions: %w", err)
	}
	for _, s := range h.Sessions() {
		// A buried session has no tab to show, so activating it would do nothing
		// visible; treating it as absent opens a fresh tab instead.
		if s.Buried {
			continue
		}
		value, ok, err := o.Terminal.GetStringVariable(ctx, iterm2.ScopeSession, s.ID, TagVariable)
		if err != nil {
			// A session that went away between the list and the read is ordinary.
			continue
		}
		if ok && value == tag {
			return s.ID, nil
		}
	}
	return "", nil
}

// openTab creates a tab running the ssh command and tags it.
func (o *Opener) openTab(ctx context.Context, s session.Session, tag string) (string, bool, error) {
	program, args, err := o.SSH.AttachCommand(s.Host, s.Tmux)
	if err != nil {
		return "", false, err
	}
	command := program + " " + strings.Join(args, " ")

	// The profile keys are iTerm2's own: "Custom Command" takes the string "Yes"
	// or "No", and "Command" is what to run. Read from iTerm2's Python library
	// (profile.py, set_use_custom_command / set_command) rather than guessed.
	// Values are JSON, so the strings carry their quotes.
	props := map[string]string{
		"Custom Command": `"Yes"`,
		"Command":        jsonString(command),
	}

	tab, err := o.Terminal.CreateTab(ctx, iterm2.CreateTabOptions{
		ProfileName:             o.Profile,
		CustomProfileProperties: props,
	})
	if err != nil {
		return "", false, fmt.Errorf("bridge: opening a tab for %s: %w", tag, err)
	}

	// Tagged after creation rather than before, because the session id does not
	// exist until the tab does. A failure here leaves a working tab that this
	// program will not recognise later, so it is reported rather than swallowed.
	if err := o.Terminal.SetStringVariable(ctx, iterm2.ScopeSession, tab.SessionID, TagVariable, tag); err != nil {
		return tab.SessionID, true, fmt.Errorf("bridge: tab for %s opened but could not be tagged, so a later click will open another: %w", tag, err)
	}
	return tab.SessionID, true, nil
}

// RegisterPanel asks iTerm2 to show the panel in its toolbelt.
//
// iTerm2 adds a tool to the visible set the first time it is registered;
// reveal asks it to be shown again on a later registration, which is what makes
// starting the bridge bring the panel back rather than leaving it hidden.
func RegisterPanel(ctx context.Context, term Terminal, url string, reveal bool) error {
	resp, err := term.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_RegisterToolRequest{
			RegisterToolRequest: &apipb.RegisterToolRequest{
				Name:                      proto.String(ToolName),
				Identifier:                proto.String(ToolIdentifier),
				ToolType:                  apipb.RegisterToolRequest_WEB_VIEW_TOOL.Enum(),
				URL:                       proto.String(url),
				RevealIfAlreadyRegistered: proto.Bool(reveal),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("bridge: registering the toolbelt panel: %w", err)
	}
	status := resp.GetRegisterToolResponse().GetStatus()
	if status != apipb.RegisterToolResponse_OK {
		return fmt.Errorf("bridge: iTerm2 declined to register the panel: %s", status)
	}
	return nil
}

// jsonString renders s as a JSON string, for a profile property value.
//
// Marshal cannot fail for a string, so the error is dropped rather than carried
// up through every caller.
func jsonString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
