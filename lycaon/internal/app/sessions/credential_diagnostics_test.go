package sessions

import (
	"context"
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
	"github.com/lycaon/lycaon/internal/tools"
)

func TestCredentialIndexEmitsValueFreeBuildAndReadinessDiagnostics(t *testing.T) {
	repochangetest.Acquire(t)
	files := credentialFilesFixture(t)
	files.startCredentialIndex()
	t.Cleanup(func() { testutil.FailErr(t, "close index", files.closeCredentialIndex(context.Background())) })
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

	// Cold Await starts 1 build ("full") and joins its BuildID in root_readiness_wait.
	coldCtx, trace := secretmatch.WithEvidenceTrace(secretmatch.WithScreenCallID(t.Context(), "call-cold"))
	testutil.FailErr(t, "cold Await", files.Await(coldCtx, tools.CredentialWait{ProjectID: "proj-1"}))
	snap := trace.Snapshot()
	if snap.RootsChecked != 1 || snap.BuildsStarted != 1 || snap.BuildsJoined != 1 || snap.DirectoriesListed < 2 || snap.PathsClassified != 3 || snap.CredentialFilesRead != 1 {
		t.Fatalf("cold trace = %+v", snap)
	}

	// Warm Await over ready root starts/joins zero builds.
	mu.Lock()
	records = nil
	mu.Unlock()
	warmCtx, warmTrace := secretmatch.WithEvidenceTrace(secretmatch.WithScreenCallID(t.Context(), "call-warm"))
	testutil.FailErr(t, "warm Await", files.Await(warmCtx, tools.CredentialWait{ProjectID: "proj-1"}))
	warmSnap := warmTrace.Snapshot()
	if warmSnap.RootsChecked != 1 || warmSnap.BuildsStarted != 0 || warmSnap.BuildsJoined != 0 || warmSnap.CredentialFilesRead != 0 {
		t.Fatalf("warm trace = %+v", warmSnap)
	}

	// Contiguous in-root edit emits incremental credential_build without walking the whole root.
	mu.Lock()
	records = nil
	mu.Unlock()
	const rotatedVal = "credential-rotated-secret-554433"
	testutil.FailErr(t, "rotate env", os.WriteFile(filepath.Join(root, ".env"), []byte("API_TOKEN="+rotatedVal+"\n"), 0o600))
	repochange.Notify(t.Context(), repochange.Event{
		ProjectDir: root,
		Kind:       repochange.WorktreeChanged,
		Paths:      []string{".env"},
	})
	testutil.FailErr(t, "await incremental", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))

	mu.Lock()
	defer mu.Unlock()
	buildCount := 0
	for idx := range records {
		if records[idx].Phase != "credential_build" {
			continue
		}
		buildCount++
		rec := records[idx]
		if rec.Mode != "incremental" || rec.DirectoriesListed != 0 ||
			rec.PathsClassified != 1 || rec.CredentialFilesRead != 1 {
			t.Fatalf("incremental build records = %+v", records)
		}
	}
	if buildCount == 0 {
		t.Fatalf("expected incremental credential_build record, got %+v", records)
	}
	raw, err := json.Marshal(records)
	testutil.FailErr(t, "marshal records", err)
	if strings.Contains(string(raw), secretVal) || strings.Contains(string(raw), rotatedVal) {
		t.Fatalf("credential diagnostic leaked secret bytes: %s", string(raw))
	}
}
