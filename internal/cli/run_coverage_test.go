package cli

import (
	"bytes"
	"testing"
)

// `run` executes project code inside the machine, so it is a mutating verb even
// though it changes no lifecycle state.
func TestRunCommandIsTreatedAsMutating(t *testing.T) {
	t.Parallel()

	mutated := false
	exitCode := Run([]string{"run", "/tmp/project", "dev"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Run: func(string, string) (int, error) {
			return 0, nil
		},
		OnMutate: func() {
			mutated = true
		},
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	if !mutated {
		t.Error("run did not report itself as a mutating command")
	}
}
