package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeHosts(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestTheHostsFileListsOneHostPerLine(t *testing.T) {
	path := writeHosts(t, "# the build farm\nbuild-box\n\ngpu-box   # the big one\n  laptop-2  \n")

	hosts, err := readHostsFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"build-box", "gpu-box", "laptop-2"}, hosts)
}

// Anything after a host name is reserved for per-host settings (ROADMAP 8b), so
// it is refused now rather than silently ignored and later given a meaning.
func TestTheHostsFileRefusesMoreThanAHostOnALine(t *testing.T) {
	path := writeHosts(t, "build-box\ngpu-box profile=Dark\n")

	_, err := readHostsFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 2")
}

func TestHostsFromTheFileAndTheFlagsAreBothWatchedOnce(t *testing.T) {
	path := writeHosts(t, "build-box\ngpu-box\n")

	hosts, err := resolveHosts([]string{"gpu-box", "extra"}, path, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"build-box", "gpu-box", "extra"}, hosts)
}

// The default file is optional: plenty of people pass --host and never write one.
func TestAMissingDefaultHostsFileIsNotAnError(t *testing.T) {
	hosts, err := resolveHosts([]string{"build-box"}, filepath.Join(t.TempDir(), "absent"), false)
	require.NoError(t, err)
	assert.Equal(t, []string{"build-box"}, hosts)
}

// One named on the command line is: the user said to read it.
func TestAMissingExplicitHostsFileIsAnError(t *testing.T) {
	_, err := resolveHosts(nil, filepath.Join(t.TempDir(), "absent"), true)
	assert.Error(t, err)
}

func TestWatchWithNoHostsNamesTheFileItLookedIn(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := runCommand(t, "watch")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--host")
	assert.Contains(t, err.Error(), filepath.Join("iterm2-claude-bridge", "hosts"))
}

func TestWatchReadsTheDefaultHostsFile(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	require.NoError(t, os.MkdirAll(filepath.Join(config, "iterm2-claude-bridge"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "iterm2-claude-bridge", "hosts"), []byte("build-box\n"), 0o600))

	hosts, err := resolveHosts(nil, defaultHostsFile(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{"build-box"}, hosts)
}
