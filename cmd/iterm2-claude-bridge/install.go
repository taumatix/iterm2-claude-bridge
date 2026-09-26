package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
	"github.com/taumatix/iterm2-claude-bridge/shared/shellquote"
)

// hookTimeoutSeconds bounds one hook invocation. Short, because a hook runs on
// the critical path of every tool call and this one only appends a line to a file.
const hookTimeoutSeconds = 5

func newInstallHooksCommand() *cobra.Command {
	var (
		stateDir string
		write    bool
		command  string
		settings string
	)

	cmd := &cobra.Command{
		Use:   "install-hooks",
		Short: "Show (or write) the Claude Code hooks that report this host's status",
		Long: `Show the hooks that make this host report Claude Code status.

By default it prints the settings.json it would write and changes nothing, because
~/.claude/settings.json is yours and may already contain hooks this program knows
nothing about. Read it, then re-run with --write to merge these hooks in; the
existing file is copied to settings.json.bak first.

Run this on each remote host, not on the Mac.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			if _, err := ensureWritableStateDir(stateDir); err != nil {
				return err
			}

			path := settings
			if path == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("finding the home directory: %w", err)
				}
				path = filepath.Join(home, ".claude", "settings.json")
			}

			existing, err := readSettings(path)
			if err != nil {
				return err
			}

			merged, added := mergeHooks(existing, command, stateDir)

			rendered, err := json.MarshalIndent(merged, "", "  ")
			if err != nil {
				return fmt.Errorf("rendering settings.json: %w", err)
			}

			if !write {
				fmt.Fprintf(out, "// %s, with %d hook event(s) added\n", path, added)
				fmt.Fprintf(out, "%s\n\n", rendered)
				fmt.Fprintln(out, "Nothing has been changed. Re-run with --write to apply it.")
				return nil
			}

			if err := writeSettings(path, rendered); err != nil {
				return err
			}
			fmt.Fprintf(out, "Wrote %s (%d hook event(s) added).\n", path, added)
			fmt.Fprintln(out, "Start Claude Code there, then run \"iterm2-claude-bridge list\" to check it is recording.")
			return nil
		},
	}

	stateDirFlag(cmd, &stateDir)
	cmd.Flags().BoolVar(&write, "write", false,
		"actually modify settings.json (a backup is made first)")
	cmd.Flags().StringVar(&command, "command", "iterm2-claude-bridge",
		"how Claude Code should invoke this program; use an absolute path if it is not on PATH")
	cmd.Flags().StringVar(&settings, "settings", "",
		"settings.json to modify (default $HOME/.claude/settings.json)")
	return cmd
}

// readSettings loads settings.json, treating a missing file as empty.
//
// It is decoded into a map so that every key this program knows nothing about
// survives the round trip. Rewriting the file from a typed struct would silently
// drop the user's other settings, which is the one thing an installer must never
// do.
func readSettings(path string) (map[string]any, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(body) == 0 {
		return map[string]any{}, nil
	}

	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, so it will not be modified: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

// mergeHooks adds this program's hooks to settings, leaving everything else
// alone, and reports how many events it added an entry to.
//
// An event that already has this program's hook is left untouched, so running
// install-hooks twice does not double every report.
func mergeHooks(settings map[string]any, command, stateDir string) (map[string]any, int) {
	// Claude Code runs a hook command through a shell, so a path containing a
	// space has to arrive as one word. Both of these come from flags, and the
	// default state directory is under $HOME — which on macOS is routinely
	// /Users/First Last. Unquoted, that hook runs the wrong program and the host
	// silently never reports.
	words := []string{command, "hook"}
	if stateDir != "" {
		words = append(words, "--state-dir", stateDir)
	}
	full := shellquote.Words(words...)

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	added := 0
	for _, event := range session.HookEventsReported() {
		entries, _ := hooks[event].([]any)
		if hooksAlreadyMention(entries, command) {
			continue
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": full,
				"timeout": hookTimeoutSeconds,
			}},
		}
		hooks[event] = append(entries, entry)
		added++
	}

	settings["hooks"] = hooks
	return settings, added
}

// hooksAlreadyMention reports whether any entry already runs command.
//
// The check is on the command rather than an exact match, so a user who edited
// the timeout or added a flag does not get a second copy.
func hooksAlreadyMention(entries []any, command string) bool {
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		list, _ := entry["hooks"].([]any)
		for _, rawHook := range list {
			hook, ok := rawHook.(map[string]any)
			if !ok {
				continue
			}
			if existing, ok := hook["command"].(string); ok && containsCommand(existing, command) {
				return true
			}
		}
	}
	return false
}

// writeSettings backs the file up and replaces it atomically.
func writeSettings(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	// Backed up before anything is replaced: this is the user's file, and a merge
	// this program got wrong should be recoverable.
	if previous, err := os.ReadFile(path); err == nil {
		backup := path + ".bak"
		if err := os.WriteFile(backup, previous, 0o600); err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	// Written to a temporary file and renamed, so a Claude Code that reads it
	// mid-write sees either the old file or the new one.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("writing settings: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing the temporary file: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}

// containsCommand reports whether existing invokes command.
func containsCommand(existing, command string) bool {
	return strings.Contains(existing, command)
}
