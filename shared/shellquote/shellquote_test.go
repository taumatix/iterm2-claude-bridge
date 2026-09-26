package shellquote_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/shared/shellquote"
)

// stubDir holds a program that reports the arguments it was given. It is created
// once, at a path chosen by this file rather than by a test name.
//
// That distinction cost a debugging round: with the stub under t.TempDir(), the
// subtest named `$(echo pwned)` put those characters into the stub's own path,
// and interpolating *that* into the command line is an injection the code under
// test cannot defend against. The stub is reached by a fixed, metacharacter-free
// name on PATH so the only unquoted text in the command line is a literal.
var stubDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "shellquote")
	if err != nil {
		panic(err)
	}
	body := "#!/bin/sh\nfor a in \"$@\"; do printf '<%s>\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(dir, "report"), []byte(body), 0o700); err != nil {
		panic(err)
	}
	// Files for a glob to find: if quoting let `*` through, the shell would expand
	// it here and the test would see the wrong arguments rather than nothing.
	for _, name := range []string{"decoy-a", "decoy-b"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			panic(err)
		}
	}
	stubDir = dir

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// oneArgument runs a real /bin/sh on a command line built from the quoted words
// and returns the arguments a program actually received.
//
// A unit test that inspects the quoted string only proves what was written. The
// question here is what a shell does with it, and the only authority on that is a
// shell.
func oneArgument(t *testing.T, words string) []string {
	t.Helper()

	cmd := exec.Command("sh", "-c", "report "+words)
	// Run where the decoys are, so a leaked glob has something to expand to.
	cmd.Dir = stubDir
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		// A leaked `$HOME` or `~` should produce something recognisably wrong.
		"HOME=/nonexistent-home")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "running %q: %s", words, out)

	// Re-join on the delimiters rather than splitting on newlines, so an argument
	// containing a newline survives the round trip intact.
	text := string(out)
	var args []string
	for len(text) > 0 {
		open := strings.Index(text, "<")
		if open < 0 {
			break
		}
		close := strings.Index(text, ">\n")
		if close < 0 {
			break
		}
		args = append(args, text[open+1:close])
		text = text[close+2:]
	}
	return args
}

// TestEveryByteSurvivesARealShell is the property the allowlist exists for: for
// any string at all, a shell hands the program one argument equal to it.
//
// Every byte value is covered rather than a hand-picked list of metacharacters,
// because a hand-picked list is exactly what an allowlist is meant to stop
// depending on. Byte 0 is excluded: a NUL cannot appear in an argv entry, so no
// quoting could carry it.
func TestEveryByteSurvivesARealShell(t *testing.T) {
	for b := 1; b < 256; b++ {
		// A newline on its own is a shell command separator, and \r is stripped by
		// some shells at end of line; both are included deliberately.
		subject := fmt.Sprintf("a%cb", b)

		t.Run(fmt.Sprintf("byte-%d", b), func(t *testing.T) {
			got := oneArgument(t, shellquote.Word(subject))
			require.Equal(t, []string{subject}, got,
				"byte %d (%q) quoted as %s", b, string(rune(b)), shellquote.Word(subject))
		})
	}
}

func TestNamesThatWouldBeCommands(t *testing.T) {
	for _, subject := range []string{
		"work",
		"my work",
		"it's",
		"a;echo pwned",
		"$(echo pwned)",
		"`echo pwned`",
		"a|echo pwned",
		"a&&echo pwned",
		"glob*",
		"*",
		"quote\"double",
		"back\\slash",
		"$HOME",
		"~",
		"~root/x",
		"#comment",
		"!bang",
		"a\nb",
		"x'y'z",
		"''",
		`'\''`,
		"é—non-ascii",
	} {
		t.Run(subject, func(t *testing.T) {
			assert.Equal(t, []string{subject}, oneArgument(t, shellquote.Word(subject)))
		})
	}
}

// TestEmptyStringIsStillAnArgument guards the case that disappears rather than
// misbehaving: an unquoted empty string leaves no argument at all, so the program
// silently receives one fewer than it was given.
func TestEmptyStringIsStillAnArgument(t *testing.T) {
	assert.Equal(t, "''", shellquote.Word(""))
	assert.Equal(t, []string{"", "after"}, oneArgument(t, shellquote.Words("", "after")))
}

// TestOrdinaryWordsAreLeftAlone is a readability guarantee, not a safety one: the
// hook command written into settings.json and the ssh command line are both things
// a human reads, and quoting every path would make them harder to check.
func TestOrdinaryWordsAreLeftAlone(t *testing.T) {
	for _, subject := range []string{
		"iterm2-claude-bridge",
		"/opt/bin/iterm2-claude-bridge",
		"/var/lib/bridge",
		"attach-session",
		"-t",
		"work",
		"host.example.com",
		"user@host",
		"KEY=value",
	} {
		assert.Equal(t, subject, shellquote.Word(subject), "%q needs no quoting", subject)
	}
}

func TestWordsQuotesEachSeparately(t *testing.T) {
	line := shellquote.Words("tmux", "select-window", "-t", "@3", ";", "attach-session", "-t", "a b")
	assert.Equal(t, `tmux select-window -t @3 ';' attach-session -t 'a b'`, line)
}
