package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

	// Log receives what a click could not do but carried on from. nil uses the
	// default logger.
	Log *slog.Logger
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
	if err := refuseExpressionSyntax(command); err != nil {
		return "", false, err
	}

	// The profile keys are iTerm2's own, read from its source rather than
	// guessed — ITAddressBookMgr.h/.m, see UPSTREAM.md:
	//
	//   "Custom Command"              KEY_CUSTOM_COMMAND, whose "Yes" is
	//                                 kProfilePreferenceCommandTypeCustomValue.
	//   "Command"                     KEY_COMMAND_LINE.
	//   "Run Command In Login Shell"  KEY_RUN_COMMAND_IN_LOGIN_SHELL, default NO.
	//
	// The last one is why ssh sees the environment the user's dotfiles build.
	// Without it iTerm2 exec's the command directly, so nothing reads ~/.zshrc
	// and a custom SSH_AUTH_SOCK — the agent holding the user's keys — is not
	// set. With it iTerm2 wraps the command in
	// `/usr/bin/login -fqpl <user> ShellLauncher --launch_shell - -i -c <cmd>`,
	// which runs the login shell interactively so the rc files are sourced.
	// iTerm2's own comment on the equivalent path names this exact case.
	//
	// Values are JSON, matching ProfileProperty.json_value, so the strings carry
	// their quotes and the boolean does not.
	props := map[string]string{
		"Custom Command":             `"Yes"`,
		"Command":                    jsonString(command),
		"Run Command In Login Shell": "true",
	}

	tab, err := o.Terminal.CreateTab(ctx, iterm2.CreateTabOptions{
		ProfileName:             o.Profile,
		WindowID:                o.currentWindow(ctx),
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

// currentWindow returns the iTerm2 window the user is looking at, or "" when
// there is none — which makes [iterm2.CreateTabOptions] open a new window.
//
// Without it every click opened a window rather than a tab, because that is
// what CreateTab does with no window id. The window wanted is the one whose
// toolbelt was just clicked, and clicking makes that window key, so the
// focus state is the answer.
//
// A failure to read it is logged and treated as "no window": a click that
// opens a tab in the wrong place is a much smaller failure than a click that
// does nothing.
func (o *Opener) currentWindow(ctx context.Context) string {
	resp, err := o.Terminal.Do(ctx, &apipb.ClientOriginatedMessage{
		Submessage: &apipb.ClientOriginatedMessage_FocusRequest{
			FocusRequest: &apipb.FocusRequest{},
		},
	})
	if err != nil {
		o.log().Warn("could not ask iTerm2 which window is in front, so opening a new one", "error", err)
		return ""
	}

	// FocusResponse carries notifications that "completely describe the state of
	// every tab and window". A window that is key is the one being used; one that
	// is merely current is the fallback for when a non-terminal window has focus,
	// which is what happens if the click arrives while something else is in front.
	var current string
	for _, n := range resp.GetFocusResponse().GetNotifications() {
		w := n.GetWindow()
		if w == nil {
			continue
		}
		switch w.GetWindowStatus() {
		case apipb.FocusChangedNotification_Window_TERMINAL_WINDOW_BECAME_KEY:
			return w.GetWindowId()
		case apipb.FocusChangedNotification_Window_TERMINAL_WINDOW_IS_CURRENT:
			current = w.GetWindowId()
		}
	}
	return current
}

// refuseExpressionSyntax rejects a command iTerm2 would evaluate rather than run.
//
// A profile's Command is an interpolated string: iTerm2 evaluates it with
// iTermExpressionEvaluator, side effects allowed, before splitting it into
// arguments (ITAddressBookMgr.m, computeCommandForProfile). Its parser starts an
// expression at a backslash followed by "(" and at nothing else
// (iTermSwiftyStringParser.m). A tmux session name is chosen on another machine,
// and shell quoting does not help here because the evaluation happens first —
// so a name containing `\(` would run an iTerm2 expression on this Mac.
//
// Refusing is the answer rather than escaping it, because getting an escape
// right means knowing how iTerm2's expression layer and its shell tokenizer
// compose, and nothing here can run either. A tmux session named with `\(` in
// it loses click-to-attach; that is the cost of failing closed.
func refuseExpressionSyntax(command string) error {
	if strings.Contains(command, `\(`) {
		return fmt.Errorf(`bridge: refusing to open a tab: the command contains \(, which iTerm2 evaluates as an expression instead of passing to the shell. The tmux session name is the usual source; renaming it is the fix`)
	}
	return nil
}

func (o *Opener) log() *slog.Logger {
	if o.Log != nil {
		return o.Log
	}
	return slog.Default()
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
