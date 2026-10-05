package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	iterm2 "github.com/taumatix/iterm2-go"

	"github.com/taumatix/iterm2-claude-bridge/domains/bridge"
	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

func newWatchCommand() *cobra.Command {
	var (
		hosts         []string
		hostsFile     string
		sshArgs       []string
		sshProgram    string
		remoteCommand string
		profile       string
		forgetAfter   time.Duration
		staleAfter    time.Duration
		verbose       bool
	)

	cmd := &cobra.Command{
		Use:   "watch [--host HOST ...]",
		Short: "Watch remote hosts and show their Claude sessions in iTerm2's toolbelt",
		Long: `Watch remote hosts and show their Claude Code sessions in iTerm2's toolbelt.

For each host it runs "ssh HOST iterm2-claude-bridge stream" and keeps a list of
what is running there. Clicking a row brings up the tab attached to that tmux
session, opening one that SSHes in and attaches if there is none.

Host names are looked up in your ~/.ssh/config, so that is where the login name,
port, jump host and identity belong.

Hosts can also be listed in a file, one per line ("#" starts a comment), by
default ~/.config/iterm2-claude-bridge/hosts. --host adds to what it lists.

iTerm2's API must be enabled: Settings > General > Magic > Enable Python API. The
first connection raises a permission prompt.`,
		Example: `  iterm2-claude-bridge watch --host build-box --host gpu-box
  iterm2-claude-bridge watch --host build-box --ssh-arg -J --ssh-arg bastion`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := hostsFile
			if path == "" {
				path = defaultHostsFile()
			}
			hosts, err := resolveHosts(hosts, path, hostsFile != "")
			if err != nil {
				return fail("%s", err)
			}
			if len(hosts) == 0 {
				return fail("no hosts to watch: pass --host, or list them one per line in %s", path)
			}

			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			}
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: level}))

			// Stopped on the first signal so a Ctrl-C tears the ssh connections down
			// rather than leaving them behind.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			ssh := bridge.SSHOptions{Program: sshProgram, Args: sshArgs}
			registry := session.NewRegistry()

			// The panel needs the Opener, the Opener needs a Terminal, and the
			// link registers the panel on every connection — so the link is
			// built first and its OnConnect reads the panel once it exists.
			var panel *bridge.Panel
			firstConnection := true
			link := &bridge.Link{
				// A fresh iterm2.Connect each time: the cookie a connection was
				// made with is spent, and iterm2-go v0.2.0 fetches a new one.
				Dial: func(ctx context.Context) (*iterm2.Conn, error) {
					return iterm2.Connect(ctx, iterm2.WithAdvisoryName("iterm2-claude-bridge"))
				},
				// iTerm2 forgets a toolbelt tool when it quits, so the panel is
				// registered on every connection. Only the first reveals it; a
				// restart should not pop the toolbelt open.
				OnConnect: func(ctx context.Context, term bridge.Terminal) error {
					reveal := firstConnection
					firstConnection = false
					return bridge.RegisterPanel(ctx, term, panel.URL(), reveal)
				},
				Log: log,
			}
			opener := &bridge.Opener{Terminal: link, SSH: ssh, Profile: profile, Log: log}

			panel, err = bridge.NewPanel(registry, opener, log)
			if err != nil {
				return err
			}
			defer panel.Close()
			panel.SetStaleAfter(staleAfter)

			if err := link.Connect(ctx); err != nil {
				return fmt.Errorf("connecting to iTerm2 (is the Python API enabled in Settings > General > Magic?): %w", err)
			}
			defer link.Close()
			// Reconnects, and registers the panel again, each time iTerm2
			// restarts — which it does on every update.
			go link.Run(ctx)
			log.Info("panel registered with iTerm2",
				"tool", bridge.ToolName,
				"hosts", hosts,
				"hint", "if you cannot see it, open View > Toolbelt")

			watcher := &bridge.Watcher{
				Registry:        registry,
				Launcher:        bridge.SSHLauncher{SSH: ssh, RemoteCommand: remoteCommand, Log: log},
				Log:             log,
				ForgetGoneAfter: forgetAfter,
			}
			watcher.Watch(ctx, hosts)

			// Watch returns when every host's goroutine has stopped, which only
			// happens on cancellation.
			log.Info("stopped")
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&hosts, "host", nil,
		"a host to watch, as named in your ssh config; repeat for several")
	cmd.Flags().StringArrayVar(&sshArgs, "ssh-arg", nil,
		"extra argument for ssh, before the destination; repeat for several")
	cmd.Flags().StringVar(&hostsFile, "hosts-file", "",
		"file listing hosts to watch, one per line (default ~/.config/iterm2-claude-bridge/hosts)")
	cmd.Flags().StringVar(&sshProgram, "ssh", "ssh", "the ssh binary to use")
	cmd.Flags().StringVar(&remoteCommand, "remote-command", bridge.DefaultRemoteCommand,
		"how to run this program on the remote host")
	cmd.Flags().StringVar(&profile, "profile", "",
		"iTerm2 profile for new tabs (default: iTerm2's default profile)")
	cmd.Flags().DurationVar(&forgetAfter, "forget-ended-after", 30*time.Second,
		"how long an ended session stays in the panel")
	cmd.Flags().DurationVar(&staleAfter, "stale-after", bridge.DefaultStaleAfter,
		"how long a working session may go without an update before its row is marked stale (0 turns it off)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "log more")

	return cmd
}
