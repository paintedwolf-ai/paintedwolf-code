package exec

import (
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentLaunchNamesScratchAndItsProgramTemp(t *testing.T) {
	scratch := t.TempDir()
	boundary := &confine.Confinement{Workspaces: confine.WorkspaceRoots{SessionScratch: scratch}, Network: confine.NetworkDeny}
	plan := AgentLaunch(LaunchAgentCommand, "fixture", boundary)
	cmd := &exec.Cmd{}
	testutil.FailErr(t, "configure child environment", configureStageCmdEnv(cmd, ExecOpts{Launch: plan, Env: []string{"TMPDIR=/unrelated", "TMP=/unrelated", "TEMP=/unrelated"}}, nil))
	temp := enginepaths.ProgramTempUnder(scratch)
	for key, want := range map[string]string{ScratchDirEnv: scratch, "TMPDIR": temp, "TMP": temp, "TEMP": temp} {
		got := ""
		for _, value := range cmd.Env {
			if len(value) > len(key) && value[:len(key)+1] == key+"=" {
				got = value[len(key)+1:]
			}
		}
		if got != want {
			t.Errorf("%s=%q want %q", key, got, want)
		}
	}
	if len(HostLaunch("maintenance").ExtraEnv) != 0 {
		t.Fatal("host maintenance acquired session state")
	}
}
