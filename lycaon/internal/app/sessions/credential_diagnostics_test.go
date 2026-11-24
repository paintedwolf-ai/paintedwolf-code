package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialIndexEmitsValueFreeBuildAndReadinessDiagnostics(t *testing.T) {
	repochangetest.Acquire(t)
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(filepath.Join(root, "config"), 0o755))
	testutil.FailErr(t, "write ordinary", os.WriteFile(filepath.Join(root, "config/app.yaml"), []byte("port: 8080\n"), 0o600))
	const secretVal = "credential-diag-secret-998877"
	testutil.FailErr(t, "write env", os.WriteFile(filepath.Join(root, ".env"), []byte("API_TOKEN="+secretVal+"\n"), 0o600))
	testutil.FailErr(t, "write template", os.WriteFile(filepath.Join(root, ".env.example"), []byte("API_TOKEN=placeholder\n"), 0o600))

	var mu sync.Mutex
	var records []secretmatch.ScreenPhaseRecord
	unsub := secretmatch.RegisterPhaseObserver(func(rec secretmatch.ScreenPhaseRecord) {
		mu.Lock()
		records = append(records, rec)
		mu.Unlock()
	})
	defer unsub()

	// Cold EnsureProject builds once ("full") and joins its BuildID in root_readiness_wait.
	coldCtx, trace := secretmatch.WithEvidenceTrace(secretmatch.WithScreenCallID(t.Context(), "call-cold"))
	testutil.FailErr(t, "cold EnsureProject", files.EnsureProject(coldCtx, "proj-1"))
	snap := trace.Snapshot()
	if snap.RootsChecked != 1 || snap.BuildsJoined != 1 || snap.DirectoriesListed < 2 || snap.PathsClassified != 3 || snap.CredentialFilesRead != 1 {
		t.Fatalf("cold trace = %+v", snap)
	}

	// Warm EnsureProject over ready root joins zero builds.
	mu.Lock()
	records = nil
	mu.Unlock()
	warmCtx, warmTrace := secretmatch.WithEvidenceTrace(secretmatch.WithScreenCallID(t.Context(), "call-warm"))
	testutil.FailErr(t, "warm EnsureProject", files.EnsureProject(warmCtx, "proj-1"))
	warmSnap := warmTrace.Snapshot()
	if warmSnap.RootsChecked != 1 || warmSnap.BuildsJoined != 0 || warmSnap.CredentialFilesRead != 0 {
		t.Fatalf("warm trace = %+v", warmSnap)
	}

	// Contiguous in-root edit emits incremental credential_build without walking the whole root.
	mu.Lock()
	records = nil
	mu.Unlock()
	const rotatedVal = "credential-rotated-secret-554433"
	testutil.FailErr(t, "rotate env", os.WriteFile(filepath.Join(root, ".env"), []byte("API_TOKEN="+rotatedVal+"\n"), 0o600))
	epoch := repochange.Advance(root)
	testutil.FailErr(t, "incremental changed", files.index.changed(t.Context(), repochange.Event{
		ProjectDir:    root,
		Kind:          repochange.WorktreeChanged,
		Paths:         []string{".env"},
		EpochBootID:   epoch.BootID,
		WorktreeEpoch: epoch.Value,
	}))

	mu.Lock()
	defer mu.Unlock()
	var buildRec *secretmatch.ScreenPhaseRecord
	for idx := range records {
		if records[idx].Phase == "credential_build" {
			buildRec = &records[idx]
		}
	}
	if buildRec == nil || buildRec.Mode != "incremental" ||
		buildRec.PathsClassified != 1 || buildRec.CredentialFilesRead != 1 {
		t.Fatalf("incremental build records = %+v", records)
	}
	raw, err := json.Marshal(records)
	testutil.FailErr(t, "marshal records", err)
	if strings.Contains(string(raw), secretVal) || strings.Contains(string(raw), rotatedVal) {
		t.Fatalf("credential diagnostic leaked secret bytes: %s", string(raw))
	}
}
