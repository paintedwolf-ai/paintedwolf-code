package exec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// StartDescriptorHelper starts a system helper that must inherit one descriptor at number 3 and
// run from its own path: a setuid helper cannot be pinned through the launch helper, which also
// occupies that descriptor. The caller's wait reaps it and is required.
func StartDescriptorHelper(ctx context.Context, launch LaunchPlan, path string, args, appendEnv []string, inherited *os.File, stderr io.Writer) (wait func() error, err error) {
	if err := launch.validate(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append([]string{"PATH=" + EffectivePathValue()}, appendEnv...)
	cmd.ExtraFiles = []*os.File{inherited}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", launch.Subject, err)
	}
	untrack := TrackProcess(cmd.Process.Pid)
	return func() error {
		defer untrack()
		return cmd.Wait()
	}, nil
}
