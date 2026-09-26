package reporter

// SetAfterReplayForTest arranges for f to run after the replay has been written
// and before the follow loop starts.
//
// That window is where a hook firing during a watcher's connect used to be lost
// by both halves of Run. Reaching it by timing alone means a test that passes on
// one machine and fails on another — which is how the bug was found, as a CI
// failure on macOS against a suite green on the machine that wrote it.
func (s *Stream) SetAfterReplayForTest(f func()) { s.afterReplay = f }
