// Command iterm2-claude-bridge shows Claude Code sessions running on remote
// hosts in iTerm2, and opens a tab that SSHes in and attaches to their tmux
// session.
//
// The same binary is both halves. On a remote host it runs as a Claude Code hook
// and as a stream; on the Mac it runs as the watcher that talks to iTerm2.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridden at build time with -ldflags "-X main.version=v0.1.0".
// Left at "dev", it is filled in from the module version `go install …@vX`
// records in the binary, which is how the README says to install.
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		// Cobra has already printed the error.
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "iterm2-claude-bridge",
		Short: "Show remote Claude Code sessions in iTerm2 and attach to them over SSH",
		Long: `Show Claude Code sessions running on remote hosts in iTerm2's toolbelt, and
open a tab that SSHes in and attaches to their tmux session when one is clicked.

The same binary runs on both sides:

  on each remote host   install-hooks, then hook (run by Claude Code) and stream
  on the Mac            watch

Start with "install-hooks --help" on a remote host.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}

	root.AddCommand(
		newHookCommand(),
		newStreamCommand(),
		newWatchCommand(),
		newInstallHooksCommand(),
		newListCommand(),
	)
	return root
}

// stateDirFlag adds the flag that selects where the event log lives, which both
// halves of the remote side need to agree on.
func stateDirFlag(cmd *cobra.Command, into *string) {
	cmd.Flags().StringVar(into, "state-dir", "",
		"directory holding the event log (default $HOME/"+defaultStateDirName+")")
}

func fail(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
