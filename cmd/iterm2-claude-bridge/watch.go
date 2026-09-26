package main

import (
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
		sshArgs       []string
		sshProgram    string
		remoteCommand string
		profile       string
		forgetAfter   time.Duration
		verbose       bool
	)

	cmd := &cobra.Command{
		Use:   "watch --host HOST [--host HOST ...]",
		Short: "Watch remote hosts and show their Claude sessions in iTerm2's toolbelt",
		Long: `Watch remote hosts and show their Claude Code sessions in iTerm2's toolbelt.

For each host it runs "ssh HOST iterm2-claude-bridge stream" and keeps a list of
what is running there. Clicking a row brings up the tab attached to that tmux
session, opening one that SSHes in and attaches if there is none.

Host names are looked up in your ~/.ssh/config, so that is where the login name,
port, jump host and identity belong.

iTerm2's API must be enabled: Settings > General > Magic > Enable Python API. The
first connection raises a permission prompt.`,
		Example: `  iterm2-claude-bridge watch --host build-box --host gpu-box
  iterm2-claude-bridge watch --host build-box --ssh-arg -J --ssh-arg bastion`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(hosts) == 0 {
				return fail("no hosts to watch: pass --host at least once")
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

			conn, err := iterm2.Connect(ctx, iterm2.WithAdvisoryName("iterm2-claude-bridge"))
			if err != nil {
				return fmt.Errorf("connecting to iTerm2 (is the Python API enabled in Settings > General > Magic?): %w", err)
			}
			defer conn.Close()

			ssh := bridge.SSHOptions{Program: sshProgram, Args: sshArgs}
			registry := session.NewRegistry()
			opener := &bridge.Opener{Terminal: conn, SSH: ssh, Profile: profile}

			panel, err := bridge.NewPanel(registry, opener, log)
			if err != nil {
				return err
			}
			defer panel.Close()

			if err := bridge.RegisterPanel(ctx, conn, panel.URL(), true); err != nil {
				return err
			}
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
	cmd.Flags().StringVar(&sshProgram, "ssh", "ssh", "the ssh binary to use")
	cmd.Flags().StringVar(&remoteCommand, "remote-command", bridge.DefaultRemoteCommand,
		"how to run this program on the remote host")
	cmd.Flags().StringVar(&profile, "profile", "",
		"iTerm2 profile for new tabs (default: iTerm2's default profile)")
	cmd.Flags().DurationVar(&forgetAfter, "forget-ended-after", 30*time.Second,
		"how long an ended session stays in the panel")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "log more")

	// Required in practice, and saying so in the flag means cobra prints the usage
	// rather than this failing at the first ssh.
	if err := cmd.MarkFlagRequired("host"); err != nil {
		// Only fails for a flag that does not exist, which would be a bug here.
		panic(err)
	}
	return cmd
}
