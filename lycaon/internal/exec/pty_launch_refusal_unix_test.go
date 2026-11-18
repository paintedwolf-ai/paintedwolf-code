//go:build darwin || linux

package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The PTY start and the pipe run refuse the same launches with the same typed
// error, and neither releases the payload.
func TestPTYAndPipeShareLaunchRefusals(t *testing.T) {
	refused := errors.New("fixture current authority refusal")
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string) LaunchPlan
		want  error
	}{
		{
			name: "unconfined agent launch while enforcing",
			setup: func(t *testing.T, _ string) LaunchPlan {
				confine.TestingSetAutoConfine(t)
				t.Setenv("LYCAON_BYPASS_APPROVALS", "")
				t.Setenv("LYCAON_SANDBOX", "")
				return AgentLaunch(LaunchAgentCommand, "fixture unconfined launch", nil)
			},
			want: confine.ErrNotConfined,
		},
		{
			name: "process inputs fail current authority",
			setup: func(_ *testing.T, root string) LaunchPlan {
				plan := AgentLaunch(LaunchAgentCommand, "fixture refused authority", &confine.Confinement{HostExecution: true, Roots: []string{root}})
				plan.CurrentAuthority = func(context.Context) error { return refused }
				return plan
			},
			want: refused,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "fixture")
			testutil.FailErr(t, "write launch fixture", os.WriteFile(entry, []byte("#!/bin/sh\nprintf released > effect\n"), 0o700))
			plan := tc.setup(t, root)

			session, err := StartPTY(t.Context(), entry, nil, PTYOpts{Launch: plan, Dir: root})
			if session != nil {
				session.Kill()
				_ = session.Wait()
				_ = session.Close()
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("StartPTY err = %v, want %v", err, tc.want)
			}
			_, err = RunPipeline(t.Context(), []Stage{{Name: entry}}, ExecOpts{Launch: plan, Dir: root})
			if !errors.Is(err, tc.want) {
				t.Fatalf("RunPipeline err = %v, want %v", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "effect")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused launch released its payload: %v", err)
			}
		})
	}
}
