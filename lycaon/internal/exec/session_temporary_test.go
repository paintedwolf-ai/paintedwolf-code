package exec

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"os/exec"
	"testing"
)

func TestAgentLaunchUsesTheSameRealSessionTemporaryDirectory(t *testing.T) {
	scratch := t.TempDir()
	boundary := &confine.Confinement{SessionScratchRoot: scratch, Network: confine.NetworkDeny}
	plan := AgentLaunch(LaunchAgentCommand, "fixture", boundary)
	cmd := &exec.Cmd{}
	testutil.FailErr(t, "configure child environment", configureStageCmdEnv(cmd, ExecOpts{Launch: plan, Env: []string{"TMPDIR=/unrelated", "TMP=/unrelated", "TEMP=/unrelated"}}, nil))
	for _, key := range []string{ScratchDirEnv, "TMPDIR", "TMP", "TEMP"} {
		got := ""
		for _, value := range cmd.Env {
			if len(value) > len(key) && value[:len(key)+1] == key+"=" {
				got = value[len(key)+1:]
			}
		}
		if got != scratch {
			t.Errorf("%s=%q want same session root %q", key, got, scratch)
		}
	}
	if len(HostLaunch("maintenance").ExtraEnv) != 0 {
		t.Fatal("host maintenance acquired session state")
	}
}
