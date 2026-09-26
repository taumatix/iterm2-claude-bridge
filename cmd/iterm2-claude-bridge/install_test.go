package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

func TestMergeHooksAddsOneEntryPerReportedEvent(t *testing.T) {
	merged, added := mergeHooks(map[string]any{}, "iterm2-claude-bridge", "")

	assert.Equal(t, len(session.HookEventsReported()), added)

	hooks, ok := merged["hooks"].(map[string]any)
	require.True(t, ok)
	for _, event := range session.HookEventsReported() {
		entries, ok := hooks[event].([]any)
		require.True(t, ok, "no entry for %s", event)
		require.Len(t, entries, 1)
	}
}

func TestMergeHooksKeepsEverySettingItDoesNotUnderstand(t *testing.T) {
	// settings.json is the user's file. Dropping their other settings is the one
	// thing an installer must never do.
	existing := map[string]any{
		"theme":                 "dark",
		"enabledPlugins":        map[string]any{"something": true},
		"nested":                map[string]any{"deep": []any{1.0, 2.0}},
		"agentPushNotifEnabled": true,
	}
	merged, _ := mergeHooks(existing, "iterm2-claude-bridge", "")

	assert.Equal(t, "dark", merged["theme"])
	assert.Equal(t, map[string]any{"something": true}, merged["enabledPlugins"])
	assert.Equal(t, map[string]any{"deep": []any{1.0, 2.0}}, merged["nested"])
	assert.Equal(t, true, merged["agentPushNotifEnabled"])
}

func TestMergeHooksKeepsHooksSomethingElseInstalled(t *testing.T) {
	existing := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{"hooks": []any{map[string]any{
					"type": "command", "command": "some-other-tool report",
				}}},
			},
		},
	}
	merged, _ := mergeHooks(existing, "iterm2-claude-bridge", "")

	entries := merged["hooks"].(map[string]any)["Stop"].([]any)
	require.Len(t, entries, 2, "the other tool's hook should survive alongside ours")

	first := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	assert.Equal(t, "some-other-tool report", first["command"])
}

func TestMergeHooksIsIdempotent(t *testing.T) {
	// Running install-hooks twice must not make every status be reported twice.
	first, added := mergeHooks(map[string]any{}, "iterm2-claude-bridge", "")
	require.Positive(t, added)

	second, addedAgain := mergeHooks(first, "iterm2-claude-bridge", "")
	assert.Zero(t, addedAgain, "nothing should be added the second time")

	hooks := second["hooks"].(map[string]any)
	for _, event := range session.HookEventsReported() {
		assert.Len(t, hooks[event].([]any), 1, "%s got a second copy", event)
	}
}

func TestMergeHooksRecognisesItsOwnHookEvenAfterTheUserEditedIt(t *testing.T) {
	// A user who changed the timeout or added a flag should not get a duplicate.
	existing := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{"hooks": []any{map[string]any{
					"type":    "command",
					"command": "/opt/bin/iterm2-claude-bridge hook --state-dir /var/tmp/x",
					"timeout": 30.0,
				}}},
			},
		},
	}
	merged, _ := mergeHooks(existing, "iterm2-claude-bridge", "")
	assert.Len(t, merged["hooks"].(map[string]any)["Stop"].([]any), 1)
}

func TestMergeHooksCarriesTheStateDirIntoTheCommand(t *testing.T) {
	// Both halves of the remote side have to agree on where the log is.
	merged, _ := mergeHooks(map[string]any{}, "/opt/bin/bridge", "/var/lib/bridge")

	entries := merged["hooks"].(map[string]any)["Stop"].([]any)
	hook := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	assert.Equal(t, "/opt/bin/bridge hook --state-dir /var/lib/bridge", hook["command"])
}

// TestMergeHooksQuotesPathsWithSpaces covers the failure that is invisible rather
// than loud: Claude Code runs the hook command through a shell, so an unquoted
// "/Users/First Last/..." runs a different program and the host simply never
// reports. The state directory defaults to a path under $HOME, and a macOS $HOME
// containing a space is ordinary.
func TestMergeHooksQuotesPathsWithSpaces(t *testing.T) {
	merged, _ := mergeHooks(map[string]any{},
		"/Users/First Last/bin/bridge", "/Users/First Last/.iterm2-claude-bridge")

	entries := merged["hooks"].(map[string]any)["Stop"].([]any)
	hook := entries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	command := hook["command"].(string)

	assert.Equal(t,
		`'/Users/First Last/bin/bridge' hook --state-dir '/Users/First Last/.iterm2-claude-bridge'`,
		command)

	// The assertion above states the quoting. This one states the consequence: a
	// real shell splits the line and the program receives the directory whole.
	//
	// The program is a stub at a space-free path so that the only quoting being
	// judged is the state directory's — pointing the hook at "/Users/First Last"
	// for real would need that directory to exist.
	stub, runWith := stubProgram(t)
	withStub, _ := mergeHooks(map[string]any{}, stub, "/Users/First Last/.iterm2-claude-bridge")
	stubEntries := withStub["hooks"].(map[string]any)["Stop"].([]any)
	stubHook := stubEntries[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)

	assert.Equal(t,
		[]string{"hook", "--state-dir", "/Users/First Last/.iterm2-claude-bridge"},
		runWith(stubHook["command"].(string)))
}

// TestMergeHooksStaysIdempotentWithAQuotedPath guards the interaction between the
// two: the duplicate check searches for the unquoted command, so quoting must not
// stop it recognising a hook it wrote itself.
func TestMergeHooksStaysIdempotentWithAQuotedPath(t *testing.T) {
	path := "/Users/First Last/bin/bridge"
	first, added := mergeHooks(map[string]any{}, path, "")
	require.Positive(t, added)

	_, addedAgain := mergeHooks(first, path, "")
	assert.Zero(t, addedAgain, "a quoted hook was not recognised on the second run")
}

// stubProgram writes a program that reports the arguments it was given, and
// returns its path along with a function that runs a hook command line through a
// real shell and reports what the program received.
//
// Claude Code executes a hook command as a shell line, so what matters is not the
// string this package builds but how a shell splits it. Only a shell can answer
// that.
func stubProgram(t *testing.T) (path string, run func(command string) []string) {
	t.Helper()

	// A space-free directory: the stub's own path must not be the thing that breaks.
	dir, err := os.MkdirTemp("", "hookstub")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	path = filepath.Join(dir, "stub")
	require.NoError(t, os.WriteFile(path,
		[]byte("#!/bin/sh\nfor a in \"$@\"; do printf '<%s>\\n' \"$a\"; done\n"), 0o700))

	run = func(command string) []string {
		t.Helper()
		out, err := exec.Command("sh", "-c", command).CombinedOutput()
		require.NoError(t, err, "running %q: %s", command, out)

		var args []string
		for _, field := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if inner, ok := strings.CutPrefix(field, "<"); ok {
				args = append(args, strings.TrimSuffix(inner, ">"))
			}
		}
		return args
	}
	return path, run
}

func TestReadSettingsTreatsAMissingFileAsEmpty(t *testing.T) {
	// Installing on a host where Claude Code has not been configured yet.
	got, err := readSettings(filepath.Join(t.TempDir(), "settings.json"))
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReadSettingsRefusesToTouchAFileItCannotParse(t *testing.T) {
	// Overwriting a file whose contents could not be read would destroy it.
	path := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(path, []byte("{ this is not json"), 0o600))

	_, err := readSettings(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "will not be modified")
}

func TestWriteSettingsBacksUpWhatWasThere(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"theme":"dark"}`)
	require.NoError(t, os.WriteFile(path, original, 0o600))

	require.NoError(t, writeSettings(path, []byte(`{"theme":"light"}`)))

	backup, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, original, backup, "a merge this program got wrong must be recoverable")

	updated, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(updated), "light")
}

func TestWriteSettingsCreatesTheDirectoryAndAPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	require.NoError(t, writeSettings(path, []byte(`{}`)))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestInstallHooksChangesNothingWithoutWrite(t *testing.T) {
	// ~/.claude/settings.json is the user's, and may contain hooks this program
	// knows nothing about, so the default is to show rather than to act.
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"theme":"dark"}`)
	require.NoError(t, os.WriteFile(path, original, 0o600))

	out, err := runCommand(t, "install-hooks", "--settings", path, "--state-dir", dir)
	require.NoError(t, err)

	assert.Contains(t, out, "Nothing has been changed")
	assert.Contains(t, out, "SessionEnd", "it should show what it would add")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, after, "the file must be untouched")
	assert.NoFileExists(t, path+".bak", "nothing was written, so nothing to back up")
}

func TestInstallHooksWritesValidSettingsWithWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600))

	out, err := runCommand(t, "install-hooks", "--write", "--settings", path, "--state-dir", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "Wrote ")

	body, err := os.ReadFile(path)
	require.NoError(t, err)

	// Claude Code has to be able to read it back.
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed), "the result must be valid JSON")
	assert.Equal(t, "dark", parsed["theme"])

	hooks, ok := parsed["hooks"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, hooks, len(session.HookEventsReported()))
}

func TestInstallHooksRefusesAnUnwritableStateDir(t *testing.T) {
	// Failing here beats failing at the first hook invocation, when nobody is
	// watching.
	dir := t.TempDir()
	readOnly := filepath.Join(dir, "ro")
	require.NoError(t, os.Mkdir(readOnly, 0o500))
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	_, err := runCommand(t, "install-hooks",
		"--settings", filepath.Join(dir, "settings.json"),
		"--state-dir", filepath.Join(readOnly, "state"))
	require.Error(t, err)
}
