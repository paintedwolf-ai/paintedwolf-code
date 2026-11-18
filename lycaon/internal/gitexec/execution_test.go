package gitexec

import (
	"strings"
	"testing"
)

func TestConcurrentExecutionsKeepTheirHostResources(t *testing.T) {
	useBundledGit(t)
	for _, command := range []string{"ssh -i first-key", "ssh -i second-key"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			e := productionExecution()
			e.hostConfig = stubHostConfig{ssh: command, helper: "fixture-helper"}
			hooks := t.TempDir()
			e.emptyHooksDir = func() (string, error) { return hooks, nil }
			dir := t.TempDir()
			for key, want := range map[string]string{"core.sshCommand": command, "core.hooksPath": hooks} {
				out, code, err := e.run(t.Context(), dir, []string{"config", "--get", key}, Opts{}, nil)
				if err != nil || code != 0 || strings.TrimSpace(string(out)) != want {
					t.Fatalf("%s: output=%q code=%d error=%v; want %q", key, out, code, err, want)
				}
			}
			_, _, err := e.run(AgentEntry(t.Context()), dir, []string{"status"}, Opts{}, nil)
			if err != ErrLaunchAuthorityRequired {
				t.Fatalf("resource instance bypassed authority: %v", err)
			}
		})
	}
}
