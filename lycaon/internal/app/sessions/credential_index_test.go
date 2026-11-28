package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
)

func attachCredentialRoot(t *testing.T, files *credentialFiles) string {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "attach fixture root", files.queries.CreateProjectRoot(t.Context(), db.CreateProjectRootParams{ID: "r1", ProjectID: "proj-1", Path: root, Label: "root", IsPrimary: 1, AddedAt: "2026-01-01T00:00:00Z", Kind: "attached"}))
	return root
}

func awaitCredentials(ctx context.Context, files *credentialFiles) error {
	return files.Await(ctx, tools.CredentialWait{ProjectID: "proj-1"})
}

func TestCredentialStartupDefersDiscoveryUntilProviderAdmission(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	path := filepath.Join(root, ".env")
	testutil.FailErr(t, "write startup credential", os.WriteFile(path, []byte("TOKEN=before-admission-4411"), 0600))
	files.startCredentialIndex()
	t.Cleanup(func() { testutil.FailErr(t, "close index", files.closeCredentialIndex(context.Background())) })
	if len(files.index.roots) != 0 || len(files.harvest.FileValues("proj-1")) != 0 || repochange.Coverage(root).Watching {
		t.Fatal("startup discovered project files or registered a filesystem watcher")
	}
	// A change before admission must be captured even without an active watcher.
	testutil.FailErr(t, "change credential before admission", os.WriteFile(path, []byte("TOKEN=at-admission-5522"), 0600))
	runtime := &security.Runtime{CredentialFiles: files}
	_, err := runtime.ModelScreeningValues(t.Context(), "proj-1", "chat")
	testutil.FailErr(t, "admit provider evidence", err)
	values := files.harvest.FileValues("proj-1")
	if len(values) != 1 || values[0].Secret() != "at-admission-5522" {
		t.Fatal("provider admission did not capture current credential evidence")
	}
	testutil.FailErr(t, "rotate admitted credential", os.WriteFile(path, []byte("TOKEN=after-admission-6633"), 0600))
	repochange.Notify(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{".env"}})
	testutil.FailErr(t, "settle refresh", awaitCredentials(t.Context(), files))
	for _, value := range files.harvest.FileValues("proj-1") {
		if value.Secret() == "after-admission-6633" && !value.Retired {
			return
		}
	}
	t.Fatal("startup subscription did not refresh the admitted project's evidence")
}

func TestCredentialIndexPrecedesAnyDelivery(t *testing.T) {
	files := credentialFilesFixture(t, "managed-value-4411")
	root := attachCredentialRoot(t, files)
	testutil.FailErr(t, "record model authorship", files.Authored(t.Context(), tools.AuthoredCredentialValues{ProjectID: "proj-1", RootID: "r1", Path: ".env", RootSessionID: "chat", SessionID: "chat", ToolCallID: "authored", Values: []string{"model-authored-5533"}}))
	body := "DB_PASSWORD=person-value-6622\nOTHER=managed-value-4411\nMODEL=model-authored-5533\n"
	testutil.FailErr(t, "write credential fixture", os.WriteFile(filepath.Join(root, ".env"), []byte(body), 0600))
	testutil.FailErr(t, "write template", os.WriteFile(filepath.Join(root, ".env.example"), []byte("DB_PASSWORD=template-value-9944"), 0600))
	exposure := 0
	files.exposure = func(context.Context, string, string) error { exposure++; return nil }
	testutil.FailErr(t, "admit project", awaitCredentials(t.Context(), files))
	got := files.harvest.FileValues("proj-1")
	if len(got) != 1 || got[0].Secret() != "person-value-6622" || exposure != 0 {
		t.Fatalf("evidence=%+v exposure=%d", got, exposure)
	}
	if len(files.harvest.FileValues("another-project")) != 0 {
		t.Fatal("file evidence crossed project scope")
	}
	if len(files.harvest.AllValues()) != 1 || len(files.harvest.ValuesForProject("proj-1")) != 1 {
		t.Fatal("unattributed or project screening omitted file evidence")
	}
}

func TestCredentialIndexRetainsRemovedBindings(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	path := filepath.Join(root, ".env")
	testutil.FailErr(t, "write first binding", os.WriteFile(path, []byte("DB_PASSWORD=first-value-4411"), 0600))
	testutil.FailErr(t, "admit root", awaitCredentials(t.Context(), files))
	testutil.FailErr(t, "rotate file binding", os.WriteFile(path, []byte("DB_PASSWORD=second-value-5533"), 0600))
	files.index.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{".env"}})
	testutil.FailErr(t, "observe changed credential", awaitCredentials(t.Context(), files))
	values := files.harvest.FileValues("proj-1")
	retired, active := false, false
	for _, value := range values {
		retired = retired || value.Secret() == "first-value-4411" && value.Retired
		active = active || value.Secret() == "second-value-5533" && !value.Retired
	}
	if !retired || !active || len(values) != 2 {
		t.Fatalf("rotation evidence = %+v", values)
	}
	testutil.FailErr(t, "delete credential fixture", os.Remove(path))
	files.index.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{".env"}})
	testutil.FailErr(t, "observe deletion", awaitCredentials(t.Context(), files))
	for _, value := range files.harvest.FileValues("proj-1") {
		if !value.Retired {
			t.Fatalf("deleted file retained active binding: %+v", value)
		}
	}
}

func TestCredentialIndexWaitsForAnAppliedChange(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	testutil.FailErr(t, "admit root", awaitCredentials(t.Context(), files))
	release, err := files.index.access.Acquire(t.Context(), credentialRootKey("proj-1", "r1"))
	testutil.FailErr(t, "hold root access", err)
	defer release()
	testutil.FailErr(t, "write credential", os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD=held-value-7788"), 0600))
	files.index.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{".env"}})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	// The runner holds the change off the queue while it waits for access.
	if err := awaitCredentials(ctx, files); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("await during an unapplied change = %v", err)
	}
}

func TestCredentialIndexDoesNotFollowLinks(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	outside := filepath.Join(t.TempDir(), ".env")
	testutil.FailErr(t, "write outside fixture", os.WriteFile(outside, []byte("DB_PASSWORD=outside-value-7733"), 0600))
	testutil.FailErr(t, "link outside fixture", os.Symlink(outside, filepath.Join(root, ".env")))
	testutil.FailErr(t, "index root", awaitCredentials(t.Context(), files))
	if len(files.harvest.FileValues("proj-1")) != 0 {
		t.Fatal("symlink supplied credential evidence outside the root")
	}
}

func TestCredentialIndexFailedPassKeepsReadBindings(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	testutil.FailErr(t, "write readable credential", os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=read-before-failure-3311"), 0600))
	testutil.FailErr(t, "create nested fixture dir", os.Mkdir(filepath.Join(root, "z"), 0700))
	file, err := os.Create(filepath.Join(root, "z", ".env"))
	testutil.FailErr(t, "create oversize fixture", err)
	testutil.FailErr(t, "size fixture", file.Truncate(credentialFileBytes+1))
	testutil.FailErr(t, "close fixture", file.Close())
	files.startCredentialIndex()
	t.Cleanup(func() { testutil.FailErr(t, "close index", files.closeCredentialIndex(context.Background())) })
	runtime := &security.Runtime{CredentialFiles: files}
	if _, err := runtime.ModelScreeningValues(t.Context(), "proj-1", "chat"); err == nil {
		t.Fatal("oversize credential capture was admitted")
	}
	if values := files.harvest.FileValues("proj-1"); len(values) != 1 || values[0].Secret() != "read-before-failure-3311" {
		t.Fatalf("failed pass dropped bindings it read: %+v", values)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := awaitCredentials(ctx, files); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture = %v", err)
	}
	testutil.FailErr(t, "repair file", os.WriteFile(filepath.Join(root, "z", ".env"), []byte("DB_PASSWORD=recovered-value-4411"), 0600))
	testutil.FailErr(t, "retry capture", awaitCredentials(t.Context(), files))
	recovered := false
	for _, value := range files.harvest.FileValues("proj-1") {
		recovered = recovered || strings.Contains(value.Secret(), "recovered") && !value.Retired
	}
	if !recovered {
		t.Fatal("recovery pass did not admit the repaired file")
	}
}

func TestCredentialIndexPartialEventCannotRepairFailedCapture(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	file, err := os.Create(filepath.Join(root, ".env"))
	testutil.FailErr(t, "create oversize", err)
	testutil.FailErr(t, "size oversize", file.Truncate(credentialFileBytes+1))
	testutil.FailErr(t, "close oversize", file.Close())
	if err := awaitCredentials(t.Context(), files); err == nil {
		t.Fatal("capture succeeded")
	}
	testutil.FailErr(t, "write another credential", os.WriteFile(filepath.Join(root, ".env.local"), []byte("TOKEN=another-value-4411"), 0600))
	files.index.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{".env.local"}})
	if err := awaitCredentials(t.Context(), files); err == nil {
		t.Fatal("failed root became ready")
	}
}

func TestCredentialIndexReconcilesMissedGenerations(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	testutil.FailErr(t, "admit root", awaitCredentials(t.Context(), files))
	testutil.FailErr(t, "write missed credential", os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=missed-value-4411"), 0600))
	repochange.Advance(root)
	epoch := repochange.Advance(root)
	files.index.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"unrelated.txt"}, EpochBootID: epoch.BootID, WorktreeEpoch: epoch.Value})
	testutil.FailErr(t, "process later event", awaitCredentials(t.Context(), files))
	values := files.harvest.FileValues("proj-1")
	if len(values) != 1 || values[0].Secret() != "missed-value-4411" {
		t.Fatalf("missed evidence=%+v", values)
	}
}

func TestCredentialIndexRevalidatesIncompleteWatchCoverage(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root := &credentialRoot{validated: now}
	incomplete := repochange.WatchCoverage{}
	if root.coverageExpired(incomplete, now.Add(repochange.CoverageRevalidationInterval-time.Nanosecond)) {
		t.Fatal("recent incomplete coverage expired")
	}
	if !root.coverageExpired(incomplete, now.Add(repochange.CoverageRevalidationInterval)) {
		t.Fatal("incomplete coverage never expired")
	}
	if root.coverageExpired(repochange.WatchCoverage{Complete: true}, now.Add(2*repochange.CoverageRevalidationInterval)) {
		t.Fatal("complete coverage needs periodic recapture")
	}
}

func TestCredentialIndexDecodesExplicitTextEncodings(t *testing.T) {
	for _, encoding := range []string{textfile.UTF8BOM, textfile.UTF16LEBOM, textfile.UTF16BEBOM} {
		t.Run(encoding, func(t *testing.T) {
			files := credentialFilesFixture(t)
			root := attachCredentialRoot(t, files)
			raw, err := textfile.EncodeBounded("TOKEN=encoded-person-value-4411", encoding, textfile.LimitsForRaw(credentialFileBytes))
			testutil.FailErr(t, "encode fixture", err)
			testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, ".env"), raw, 0600))
			testutil.FailErr(t, "capture encoded file", awaitCredentials(t.Context(), files))
			values := files.harvest.FileValues("proj-1")
			if len(values) != 1 || values[0].Secret() != "encoded-person-value-4411" {
				t.Fatal("encoded credential binding was not admitted")
			}
		})
	}
}

func TestCredentialIndexEpochOnlyEventsSkipWalks(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	complete := false
	files.index.coverage = func(string) repochange.WatchCoverage { return repochange.WatchCoverage{Complete: complete} }
	testutil.FailErr(t, "admit root", awaitCredentials(t.Context(), files))
	passes := func() int {
		files.index.mu.Lock()
		defer files.index.mu.Unlock()
		return files.index.roots[credentialRootKey("proj-1", "r1")].passes
	}
	notify := func(kind repochange.Kind) {
		epoch := repochange.Advance(root)
		files.index.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: kind, EpochBootID: epoch.BootID, WorktreeEpoch: epoch.Value})
		testutil.FailErr(t, "settle event", awaitCredentials(t.Context(), files))
	}
	notify(repochange.IndexChanged)
	if got := passes(); got != 1 {
		t.Fatalf("index change walked the root: passes=%d", got)
	}
	complete = true
	notify(repochange.HeadMoved)
	if got := passes(); got != 1 {
		t.Fatalf("head move under complete coverage walked the root: passes=%d", got)
	}
	complete = false
	notify(repochange.HeadMoved)
	if got := passes(); got != 2 {
		t.Fatalf("head move under incomplete coverage skipped its walk: passes=%d", got)
	}
}

func TestCredentialIndexBatchesLargeRoots(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	for n := range credentialBatchFiles + 3 {
		dir := filepath.Join(root, fmt.Sprintf("d%03d", n))
		testutil.FailErr(t, "create fixture dir", os.Mkdir(dir, 0700))
		testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(dir, ".env"), []byte(fmt.Sprintf("TOKEN=batched-value-%04d", n)), 0600))
	}
	testutil.FailErr(t, "admit root", awaitCredentials(t.Context(), files))
	if got := len(files.harvest.FileValues("proj-1")); got != credentialBatchFiles+3 {
		t.Fatalf("bindings=%d", got)
	}
}
