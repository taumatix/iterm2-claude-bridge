package reporter

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/taumatix/iterm2-claude-bridge/domains/session"
)

// Against the real ps of the machine running the test.
func TestProcessAliveComparesTheStartTimeNotOnlyThePID(t *testing.T) {
	ctx := context.Background()
	self, ok, err := inspect(ctx, os.Getpid())
	require.NoError(t, err)
	require.True(t, ok)

	alive, err := ProcessAlive(ctx, session.Process{PID: os.Getpid(), Started: self.started})
	require.NoError(t, err)
	assert.True(t, alive)

	// The same pid with another start time is a reused pid: a different process.
	alive, err = ProcessAlive(ctx, session.Process{PID: os.Getpid(), Started: "Thu Jan  1 00:00:00 1970"})
	require.NoError(t, err)
	assert.False(t, alive, "a reused pid was taken for the process that had it before")
}

func TestProcessAliveSaysNoForAnExitedProcess(t *testing.T) {
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())

	alive, err := ProcessAlive(context.Background(), session.Process{PID: cmd.Process.Pid, Started: "x"})
	require.NoError(t, err, "an exited process is an answer, not an error")
	assert.False(t, alive)
}

func TestParsePSReadsBothPlatformsOutput(t *testing.T) {
	for _, tc := range []struct {
		name, out, command, started string
		ppid                        int
	}{
		{"linux", "  812 Sun Oct  4 02:05:01 2026 bash\n", "bash", "Sun Oct 4 02:05:01 2026", 812},
		{"macos path", "  1 Sun Oct  4 02:05:01 2026 /bin/zsh\n", "zsh", "Sun Oct 4 02:05:01 2026", 1},
		{"login shell", "77 Sun Oct  4 02:05:01 2026 -zsh", "zsh", "Sun Oct 4 02:05:01 2026", 77},
		{"space in path", "9 Sun Oct  4 02:05:01 2026 /Applications/My App/claude", "claude", "Sun Oct 4 02:05:01 2026", 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, ok, err := parsePS(tc.out)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tc.ppid, info.ppid)
			assert.Equal(t, tc.started, info.started)
			assert.Equal(t, tc.command, info.command)
		})
	}
	_, _, err := parsePS("garbage")
	assert.Error(t, err)
}
