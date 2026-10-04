package reporter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// Which process a hook ran for, and whether it is still running.
//
// ps is used rather than /proc or sysctl because the remote half runs on Linux
// and macOS alike, and `ps -o` with these three keywords means the same on
// both. Its output is read under LC_ALL=C so that the start time is formatted
// the same way by the hook that records it and the stream that compares it.

// shells are the programs Claude Code may run a hook through. Claude runs a
// hook's command with `sh -c`, and a user's command can be a script with a
// shell of its own, so the process a hook reports for is its nearest ancestor
// that is none of these.
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"fish": true, "tcsh": true, "csh": true, "ash": true,
}

// maxAncestors bounds the walk up the process tree.
const maxAncestors = 8

// ClaudeProcess finds the process this hook is running for: the nearest
// ancestor of this one that is not a shell.
func ClaudeProcess(ctx context.Context) (*session.Process, error) {
	pid := os.Getppid()
	for i := 0; i < maxAncestors && pid > 1; i++ {
		info, ok, err := inspect(ctx, pid)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("reporter: process %d exited while the hook was looking at it", pid)
		}
		if !shells[info.command] {
			return &session.Process{PID: pid, Started: info.started}, nil
		}
		pid = info.ppid
	}
	return nil, errors.New("reporter: no ancestor of the hook is anything but a shell")
}

// ProcessAlive reports whether p is still running: a process with its pid
// exists and started when p did.
func ProcessAlive(ctx context.Context, p session.Process) (bool, error) {
	info, ok, err := inspect(ctx, p.PID)
	if err != nil || !ok {
		return false, err
	}
	return info.started == p.Started, nil
}

type processInfo struct {
	ppid    int
	started string
	command string
}

// inspect asks ps about one pid. ok is false when there is no such process.
func inspect(ctx context.Context, pid int) (info processInfo, ok bool, err error) {
	cmd := exec.CommandContext(ctx, "ps", "-o", "ppid=,lstart=,comm=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && strings.TrimSpace(string(out)) == "" {
		// ps exits 1 with nothing to say for a pid that does not exist, on both
		// Linux and macOS.
		return processInfo{}, false, nil
	}
	if err != nil {
		return processInfo{}, false, fmt.Errorf("reporter: running ps for %d: %w", pid, err)
	}
	return parsePS(string(out))
}

// parsePS reads one line of `ps -o ppid=,lstart=,comm=`: the parent pid, a
// start time of five fields ("Sun Oct  4 02:05:01 2026"), and the command,
// which may itself contain spaces.
func parsePS(out string) (processInfo, bool, error) {
	fields := strings.Fields(out)
	if len(fields) < 7 {
		return processInfo{}, false, fmt.Errorf("reporter: unexpected ps output %q", out)
	}
	ppid, err := strconv.Atoi(fields[0])
	if err != nil {
		return processInfo{}, false, fmt.Errorf("reporter: unexpected ps output %q", out)
	}
	command := filepath.Base(strings.Join(fields[6:], " "))
	// A login shell is listed with a leading dash.
	command = strings.TrimPrefix(command, "-")
	return processInfo{ppid: ppid, started: strings.Join(fields[1:6], " "), command: command}, true, nil
}
