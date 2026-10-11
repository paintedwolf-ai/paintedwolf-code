package sessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/session/store"
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

func TestUntrustedIngestionProjectionReadsLiveEvidenceAndRejectsCanceledRead(t *testing.T) {
	memory := store.NewMemory()
	session, err := memory.Create(t.Context(), api.CreateSessionRequest{}, "fixture-project")
	testutil.FailErr(t, "create evidence projection session", err)
	projection := untrustedIngestionStore{store: memory}
	if projection.SessionIngestedUntrusted(t.Context(), session.ID) {
		t.Fatal("empty evidence ledger was declared untrusted")
	}
	testutil.FailErr(t, "seed inherited untrusted evidence", memory.SeedUntrustedContent(t.Context(), session.ID))
	if !projection.SessionIngestedUntrusted(t.Context(), session.ID) || projection.SessionIngestedUntrusted(t.Context(), "other") {
		t.Fatal("untrusted evidence crossed chat attribution or remained hidden")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if projection.SessionIngestedUntrusted(canceled, session.ID) || (untrustedIngestionStore{}).SessionIngestedUntrusted(t.Context(), session.ID) {
		t.Fatal("failed or missing evidence read invented an observation")
	}
}
