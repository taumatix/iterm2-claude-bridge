package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The hosts file lists the hosts `watch` follows, one per line, so watching a
// dozen machines does not mean a dozen --host flags. Blank lines are skipped and
// "#" starts a comment.
//
// A line holds a host name and nothing else. Anything after it is refused rather
// than ignored, so that per-host settings can be given that syntax later without
// changing what an existing file means.

// defaultHostsFile is $XDG_CONFIG_HOME/iterm2-claude-bridge/hosts, or
// ~/.config/iterm2-claude-bridge/hosts. ~/.config rather than macOS's
// Application Support, because it is where a person editing it by hand looks.
func defaultHostsFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "iterm2-claude-bridge", "hosts")
}

func readHostsFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hosts []string
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		switch len(fields) {
		case 0:
			continue
		case 1:
			hosts = append(hosts, fields[0])
		default:
			return nil, fmt.Errorf("%s, line %d: one host per line; per-host settings are not supported yet", path, n)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return hosts, nil
}

// resolveHosts is the hosts file's hosts followed by the --host ones, each once.
// A file that does not exist is an error only when it was named explicitly.
func resolveHosts(flagHosts []string, path string, explicit bool) ([]string, error) {
	var fromFile []string
	if path != "" {
		var err error
		fromFile, err = readHostsFile(path)
		if err != nil && (explicit || !errors.Is(err, os.ErrNotExist)) {
			return nil, err
		}
	}

	seen := make(map[string]bool)
	var hosts []string
	for _, h := range append(fromFile, flagHosts...) {
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts, nil
}
