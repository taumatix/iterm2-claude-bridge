package main_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// TestWorkflowsParseAndHaveSteps catches a malformed workflow here rather than
// on the push that needed it.
//
// A missing colon after "steps" is valid YAML — it becomes the string "steps"
// inside a list — so GitHub accepts the file and silently runs a job with no
// steps, or rejects it only once pushed. Parsing it with a real YAML parser and
// asserting the shape is the cheap way to know before then; a hand-rolled check
// over the text got this wrong twice, flagging shell keywords inside run blocks.
func TestWorkflowsParseAndHaveSteps(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(".github", "workflows", "*.yml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no workflows found; CI would not run")

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			require.NoError(t, err)

			var workflow struct {
				Name string `yaml:"name"`
				Jobs map[string]struct {
					RunsOn any `yaml:"runs-on"`
					Steps  []struct {
						Name string `yaml:"name"`
						Uses string `yaml:"uses"`
						Run  string `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal(body, &workflow), "%s is not valid YAML", path)

			assert.NotEmpty(t, workflow.Name)
			require.NotEmpty(t, workflow.Jobs, "%s defines no jobs", path)

			for name, job := range workflow.Jobs {
				assert.NotNil(t, job.RunsOn, "job %q has no runs-on", name)
				require.NotEmpty(t, job.Steps, "job %q has no steps", name)

				for i, step := range job.Steps {
					assert.True(t, step.Uses != "" || step.Run != "",
						"job %q step %d (%q) neither uses an action nor runs anything",
						name, i, step.Name)
				}
			}
		})
	}
}
