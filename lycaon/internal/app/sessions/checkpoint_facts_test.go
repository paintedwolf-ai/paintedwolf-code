package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointConsequenceFactsKeepSecretDetectionAndWriteSubjectsDistinct(t *testing.T) {
	paths, err := approvals.LoadConsequenceBandPaths()
	testutil.FailErr(t, "load consequence destinations", err)
	derive := checkpointConsequenceDeriver{dests: paths}
	for _, tc := range []struct {
		level  string
		secret bool
		code   api.ConsequenceCode
	}{
		{"info", false, ""}, {"critical", false, api.ConsequenceCodeDetection}, {"info", true, api.ConsequenceCodeSecret}, {"critical", true, api.ConsequenceCodeSecret},
	} {
		band, code := derive.ToolApproval(tc.level, tc.secret)
		if code != tc.code || (band == api.ConsequenceBandHighRisk) != (tc.code != "") {
			t.Fatalf("consequence level=%q secret=%v =>%q,%q", tc.level, tc.secret, band, code)
		}
	}
	if band, code := derive.WriteRoot(t.TempDir()); band != "" || code != "" {
		t.Fatalf("ordinary temporary root received high-risk presentation:%q,%q", band, code)
	}
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve high-impact fixture home", err)
	if band, code := derive.WriteRoot(filepath.Join(home, ".local", "bin")); band != api.ConsequenceBandHighRisk || code != api.ConsequenceCodeWriteRoot {
		t.Fatalf("cataloged program directory lost write-root consequence:%q,%q", band, code)
	}
}
