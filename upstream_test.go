package main_test

import (
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// upstreamPin is one entry of the yaml block in UPSTREAM.md, which is the only
// place a pin is recorded so that nothing can hold a second copy and disagree.
type upstreamPin struct {
	Name    string `yaml:"name"`
	Kind    string `yaml:"kind"`
	Checked string `yaml:"checked"`
}

var yamlBlock = regexp.MustCompile("(?s)```yaml\n(.*?)\n```")

func readPins(t *testing.T) []upstreamPin {
	t.Helper()
	doc, err := os.ReadFile("UPSTREAM.md")
	require.NoError(t, err, "UPSTREAM.md must exist: a version a user cannot see is one nobody can check")

	match := yamlBlock.FindSubmatch(doc)
	require.NotNil(t, match, "UPSTREAM.md has no ```yaml block for the pins")

	var pins []upstreamPin
	require.NoError(t, yaml.Unmarshal(match[1], &pins), "the pin block is not valid yaml")
	return pins
}

func TestEveryUpstreamPinRecordsWhenItWasChecked(t *testing.T) {
	// An unrefreshed date is indistinguishable from an unchecked one, so every pin
	// carries one and it has to parse as a date.
	pins := readPins(t)
	require.NotEmpty(t, pins)

	for _, pin := range pins {
		t.Run(pin.Name, func(t *testing.T) {
			require.NotEmpty(t, pin.Name, "a pin with no name cannot be referred to")
			require.NotEmpty(t, pin.Kind)
			require.NotEmpty(t, pin.Checked, "no checked date")

			_, err := time.Parse(time.DateOnly, pin.Checked)
			assert.NoError(t, err, "checked must be YYYY-MM-DD")
		})
	}
}

func TestEverythingThisProgramDependsOnIsPinned(t *testing.T) {
	// Each of these is something outside this repo that can change without us. A
	// dependency nobody wrote down is one nobody checks.
	pins := readPins(t)
	named := map[string]bool{}
	for _, pin := range pins {
		named[pin.Name] = true
	}

	for _, want := range []string{
		"claude-code-hooks",              // the hook event names and payload
		"iterm2-profile-keys",            // how a tab is told what to run
		"iterm2-claude-code-integration", // what this complements
		"tmux",                           // format strings and the ; separator
		"openssh",                        // the transport
		"iterm2-go",                      // the iTerm2 client
	} {
		assert.True(t, named[want], "UPSTREAM.md has no pin for %s", want)
	}
}
