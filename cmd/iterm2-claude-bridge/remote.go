package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/taumatix/iterm2-claude-bridge/domains/reporter"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// defaultStateDirName is repeated from the reporter package for the flag's help
// text, which has to read as a path rather than as a constant.
const defaultStateDirName = reporter.DefaultStateDir

func newHookCommand() *cobra.Command {
	var stateDir string

	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Record one status change (run by Claude Code, not by hand)",
		Long: `Record one status change from a Claude Code hook.

Claude Code runs this with the hook payload on standard input. It is installed by
"install-hooks".

It always exits 0. A hook that fails must not stop Claude from working, so a
problem is reported on standard error and nothing else happens — the worst case is
a row in the panel that does not update.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := reporter.NewStore(stateDir)
			if err != nil {
				// Reported, not returned: see the note on exit codes above.
				fmt.Fprintf(cmd.ErrOrStderr(), "iterm2-claude-bridge: %v\n", err)
				return nil
			}

			hook := &reporter.Hook{Store: store, Tmux: reporter.NewTmuxResolver(), Process: reporter.ClaudeProcess}
			event, err := hook.Handle(cmd.Context(), cmd.InOrStdin())
			switch {
			case errors.Is(err, reporter.ErrNotAStatusChange):
				// The common case for most events. Nothing to say.
			case err != nil:
				fmt.Fprintf(cmd.ErrOrStderr(), "iterm2-claude-bridge: %v\n", err)
			default:
				// Only when asked: a hook writing to stdout on every tool call would
				// clutter the transcript.
				if verbose, _ := cmd.Flags().GetBool("verbose"); verbose {
					fmt.Fprintf(cmd.ErrOrStderr(), "recorded %s for %s\n", event.Status, event.SessionID)
				}
			}
			return nil
		},
	}

	stateDirFlag(cmd, &stateDir)
	cmd.Flags().Bool("verbose", false, "report what was recorded on standard error")
	return cmd
}

func newStreamCommand() *cobra.Command {
	var (
		stateDir       string
		poll           time.Duration
		heartbeat      time.Duration
		checkProcesses time.Duration
	)

	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Stream this host's status changes as newline-delimited JSON",
		Long: `Stream this host's Claude Code status changes, one JSON object per line.

The current state of every session is written first, so a watcher that has just
connected sees everything rather than only what changes from now on. Then it
follows the log until it is stopped.

This is what the Mac runs over SSH; it is not usually run by hand. Standard output
is the protocol, so nothing else is written there.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := reporter.NewStore(stateDir)
			if err != nil {
				return err
			}

			// SIGPIPE is the ordinary way this ends: the watcher goes away and the
			// next write fails. SIGTERM and SIGINT come from a human or an ssh
			// teardown.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			stream := &reporter.Stream{
				Store:        store,
				PollInterval: poll,
				Heartbeat:    heartbeat,

				Hello: &session.Hello{
					Protocol:       session.Protocol,
					Version:        version,
					CheckProcesses: checkProcesses > 0,
				},
				CheckProcesses: checkProcesses,
				Alive:          reporter.ProcessAlive,
			}
			return stream.Run(ctx, cmd.OutOrStdout())
		},
	}

	stateDirFlag(cmd, &stateDir)
	cmd.Flags().DurationVar(&poll, "poll", reporter.DefaultPollInterval,
		"how often to look for new events")
	cmd.Flags().DurationVar(&checkProcesses, "check-processes", reporter.DefaultCheckProcesses,
		"how often to look for sessions whose Claude has exited without saying so (0 disables)")
	cmd.Flags().DurationVar(&heartbeat, "heartbeat", 60*time.Second,
		"write a blank line after this long with nothing to say, so an idle connection is not reaped (0 disables)")
	return cmd
}

func newListCommand() *cobra.Command {
	var stateDir string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print this host's Claude Code sessions and exit",
		Long: `Print what this host currently knows about its Claude Code sessions.

For checking that the hooks are recording anything, without needing iTerm2 or a
watcher. Run it on the remote host after using Claude there.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := reporter.NewStore(stateDir)
			if err != nil {
				return err
			}
			events, err := store.Replay()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(events) == 0 {
				fmt.Fprintf(out, "No sessions recorded in %s.\n", store.Path)
				fmt.Fprintln(out, "If Claude has run here since install-hooks, check that the hook command is on PATH.")
				return nil
			}
			for _, e := range events {
				tmux := e.Tmux.Session
				if tmux == "" {
					tmux = "(not under tmux)"
				}
				fmt.Fprintf(out, "%-8s  %-28s  %-20s  %s\n",
					e.Status, e.SessionID, tmux, e.Cwd)
			}
			return nil
		},
	}

	stateDirFlag(cmd, &stateDir)
	return cmd
}

// ensureWritableStateDir is used by install-hooks to fail early rather than at
// the first hook invocation, when nobody is watching.
func ensureWritableStateDir(stateDir string) (*reporter.Store, error) {
	store, err := reporter.NewStore(stateDir)
	if err != nil {
		return nil, err
	}
	probe := store.Path + ".probe"
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return nil, fmt.Errorf("the state directory is not writable: %w", err)
	}
	return store, os.Remove(probe)
}
