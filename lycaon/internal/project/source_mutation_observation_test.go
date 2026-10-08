package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func pendingObservationWrite(t *testing.T, status sourceMutationStatus) (*SourceMutationService, *Project, *sourceMutationRow) {
	t.Helper()
	service, p, root, _ := sourceMutationFixture(t)
	for _, name := range []string{"a.txt", "unrelated.txt"} {
		testutil.FailErr(t, "seed tracked file", os.WriteFile(filepath.Join(root, name), []byte("before"), 0o600))
		testutil.FailErr(t, "record tracked file", service.ledger.Record(t.Context(), sourceledger.RecordInput{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: name, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser, OperationID: uuid.NewString(), After: []byte("before")}))
	}
	write, err := planProjectSourceWrite(p, SourceWriteRequest{RootID: p.Roots[0].ID, Path: "a.txt", Content: "in app", Encoding: textfile.UTF8, BaseSHA256: textfile.SHA256([]byte("before"))})
	testutil.FailErr(t, "plan pending write", err)
	now := time.Now().UTC()
	row := &sourceMutationRow{ID: uuid.NewString(), ProjectID: p.ID, Kind: "write", InputDigest: "input", Status: status, CreatedAt: now, UpdatedAt: now,
		Plan: sourceMutationPlan{Kind: "write", ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), RootID: p.Roots[0].ID, RootPath: root, Path: "a.txt", AbsPath: write.Result.AbsPath, Before: write.Result.Before, After: write.Result.After, BaseSHA256: write.BaseSHA256, AfterSHA: write.Result.SHA256, Changed: true, Response: json.RawMessage(`{}`)}}
	testutil.FailErr(t, "persist pending write", service.insert(t.Context(), row))
	testutil.FailErr(t, "publish before attribution", applyProjectSourceWrite(write))
	return service, p, row
}

func observeMutationPaths(t *testing.T, service *SourceMutationService, p *Project, paths ...string) int {
	t.Helper()
	refs := make([]sourceledger.PathRef, 0, len(paths))
	for _, path := range paths {
		refs = append(refs, sourceledger.PathRef{RootID: p.Roots[0].ID, Path: path})
	}
	count, err := service.ledger.ObservePaths(t.Context(), p.ID, []sourceledger.RootSpec{{ID: p.Roots[0].ID, Path: p.Roots[0].Path}}, refs)
	testutil.FailErr(t, "observe tracked paths", err)
	return count
}

func TestPendingMutationDefersOnlyItsAffectedPath(t *testing.T) {
	for _, status := range []sourceMutationStatus{sourceMutationPrepared, sourceMutationFileApplied} {
		t.Run(string(status), func(t *testing.T) {
			service, p, row := pendingObservationWrite(t, status)
			testutil.FailErr(t, "write unrelated outside change", os.WriteFile(filepath.Join(p.Roots[0].Path, "unrelated.txt"), []byte("outside"), 0o600))
			if count := observeMutationPaths(t, service, p, "a.txt", "unrelated.txt"); count != 1 {
				t.Fatalf("pending operation suppressed unrelated change: recorded=%d", count)
			}
			testutil.FailErr(t, "commit pending attribution", service.commit(t.Context(), row))
			if count := observeMutationPaths(t, service, p, "a.txt", "unrelated.txt"); count != 0 {
				t.Fatalf("committed write received duplicate attribution: %d", count)
			}
			testutil.FailErr(t, "write later outside change", os.WriteFile(filepath.Join(p.Roots[0].Path, "a.txt"), []byte("outside later"), 0o600))
			if count := observeMutationPaths(t, service, p, "a.txt"); count != 1 {
				t.Fatal("resolved mutation suppressed later outside write")
			}
		})
	}
}

type pausedMutationScope struct {
	service  *SourceMutationService
	observed chan struct{}
	resume   chan struct{}
	once     sync.Once
}

func (p *pausedMutationScope) ObservationScope(ctx context.Context, tx *sql.Tx, projectID string) (sourceledger.MutationObservationScope, error) {
	pending, err := p.service.ObservationScope(ctx, tx, projectID)
	if pending != nil && err == nil {
		p.once.Do(func() {
			close(p.observed)
			select {
			case <-p.resume:
			case <-ctx.Done():
			}
		})
	}
	return pending, err
}

func TestMutationCommitRacingObservationDoesNotCreateOutsideEffect(t *testing.T) {
	service, p, row := pendingObservationWrite(t, sourceMutationFileApplied)
	scope := &pausedMutationScope{service: service, observed: make(chan struct{}), resume: make(chan struct{})}
	service.ledger.SetMutationScopeProvider(scope)
	type result struct {
		count int
		err   error
	}
	finished := make(chan result, 1)
	go func() {
		count, err := service.ledger.ObservePaths(t.Context(), p.ID, []sourceledger.RootSpec{{ID: p.Roots[0].ID, Path: p.Roots[0].Path}}, []sourceledger.PathRef{{RootID: p.Roots[0].ID, Path: "a.txt"}})
		finished <- result{count, err}
	}()
	select {
	case <-scope.observed:
	case <-time.After(5 * time.Second):
		close(scope.resume)
		t.Fatal("observation did not read pending registry")
	}
	commitFinished := make(chan error, 1)
	commitStarted := make(chan struct{})
	go func() {
		close(commitStarted)
		commitFinished <- service.commit(t.Context(), row)
	}()
	<-commitStarted
	close(scope.resume)
	testutil.FailErr(t, "commit racing the observation transaction", <-commitFinished)
	observed := <-finished
	testutil.FailErr(t, "finish racing observation", observed.err)
	if observed.count != 0 {
		t.Fatal("racing commit produced outside attribution")
	}
	if count := observeMutationPaths(t, service, p, "a.txt"); count != 0 {
		t.Fatal("retry duplicated committed user write")
	}
}

func TestMutationObservationScopeIncludesRenameAndBatchDescendants(t *testing.T) {
	plan := sourceMutationPlan{Kind: "batch_write", Changed: true, Writes: []sourceMutationPlan{
		{Kind: "rename", RootID: "root", Path: "new", FromPath: "old", Changed: true},
		{Kind: "write", RootID: "root", Path: "other/file", Changed: true},
	}}
	for _, path := range []string{"old/file", "new/file", "other/file"} {
		if !pendingMutationObservations(plan.observationPaths()).Pending(sourcebranch.Trunk, "root", path) {
			t.Fatalf("pending mutation did not cover %s", path)
		}
	}
	for _, path := range []string{"oldish/file", "unrelated"} {
		if pendingMutationObservations(plan.observationPaths()).Pending(sourcebranch.Trunk, "root", path) {
			t.Fatalf("pending mutation covered unrelated %s", path)
		}
	}
	if pendingMutationObservations(plan.observationPaths()).Pending(sourcebranch.ForWorktree("other"), "root", "new/file") || pendingMutationObservations(plan.observationPaths()).Pending(sourcebranch.Trunk, "other-root", "new/file") {
		t.Fatal("pending mutation crossed branch or root")
	}
}
