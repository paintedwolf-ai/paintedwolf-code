//go:build darwin || linux

package toolhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A host resource's executable directory joins PATH only at the child that
// declares it, so the program's own unmodified PATH must launch and find it.
func TestProgramHostResourceExecutableLaunchesFromTheDeclaringChild(t *testing.T) {
	env := newPipeSemanticsEnv(t)
	install := t.TempDir()
	tool := filepath.Join(install, "pw-resource-tool")
	testutil.FailErr(t, "install resource tool", os.WriteFile(tool, []byte("#!/bin/sh\necho resource-ran\n"), 0o755))
	env.Runtime.Executor.Network.SetHostResourceConnectionSource(func(_ context.Context, ids []string, _ hostresources.ProjectContext, _ []hostresources.ExecutionSurface) (hostresources.ActionResolution, []string) {
		states := map[string]hostresources.State{}
		for _, id := range ids {
			states[id] = hostresources.State{ID: id, Family: "fixture.local"}
		}
		return hostresources.ActionResolution{States: states, Families: []string{"fixture.local"}, PathExtra: []string{install}}, nil
	})
	for source, want := range map[string]string{
		"pw-resource-tool":             "resource-ran\n",
		"external -- pw-resource-tool": "resource-ran\n",
		"which pw-resource-tool":       tool + "\n",
		"command -v pw-resource-tool":  tool + "\n",
		"FIXTURE=1 pw-resource-tool":   "resource-ran\n",
		"pw-resource-tool | cat":       "resource-ran\n",
	} {
		out, err := env.CallArgs(t, t.Context(), map[string]any{
			"command":            source,
			"capability_request": map[string]any{"host_resources": []any{"fixture"}},
		})
		testutil.FailErr(t, "run "+source, err)
		if result := decodePipeOutcome(t, out); result.Status != 0 || result.Refusal != nil || result.Stdout != want {
			t.Errorf("%s: %+v, want stdout %q", source, result, want)
		}
	}
}
