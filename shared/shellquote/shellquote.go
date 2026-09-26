// Package shellquote renders strings as single words for a POSIX shell.
//
// Two places in this program build a command line that a shell parses before
// the intended program sees it: the remote half of an ssh command, and the hook
// command written into Claude Code's settings.json. In both, a string that came
// from somewhere else — a tmux session name reported by another machine, a path
// a user passed on the command line — must arrive as exactly one argument.
package shellquote

import "strings"

// safe lists the bytes that need no quoting anywhere in a POSIX shell word.
//
// It is an allowlist, not a list of metacharacters to escape. The difference
// matters: with a denylist, a byte nobody thought of is passed through bare, and
// the cost of being wrong is a remote-supplied string becoming shell syntax.
// With an allowlist the same oversight only means a redundant pair of quotes.
//
// Every byte outside this set is quoted, including ones that are harmless in
// argument position (`~`, `#`, `=` beyond the first word). Quoting a byte that
// did not need it changes nothing about what the command does.
const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"abcdefghijklmnopqrstuvwxyz" +
	"0123456789" +
	"%+,-./:=@_"

// Word renders s so a POSIX shell hands the command exactly one argument equal
// to s, byte for byte.
//
// Single quotes disable every expansion — parameter, command, arithmetic,
// globbing, tilde and history. The one byte they cannot carry is a single quote,
// which is closed, escaped outside the quotes, and reopened.
func Word(s string) string {
	if s == "" {
		// Without this an empty string would vanish from the command line rather
		// than being passed as an empty argument.
		return "''"
	}
	if isSafe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Words renders each of ss as a shell word and joins them with spaces.
func Words(ss ...string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = Word(s)
	}
	return strings.Join(quoted, " ")
}

// isSafe reports whether every byte of s is in the allowlist.
//
// Indexed by byte rather than ranged over as runes on purpose: a multi-byte rune
// has no byte in the allowlist, so any non-ASCII string is quoted. That is the
// conservative answer, and it keeps the check independent of whether s is valid
// UTF-8 at all.
func isSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(safe, rune(s[i])) {
			return false
		}
	}
	return true
}
