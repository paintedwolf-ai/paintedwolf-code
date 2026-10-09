package projectsource

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
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
		testutil.FailErr(t, "record tracked file", service.settlement.recorder.(*sourceledger.Store).Record(t.Context(), sourceledger.RecordInput{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: name, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser, OperationID: uuid.NewString(), After: []byte("before")}))
	}
	write, err := planProjectSourceWrite(p, SourceWriteRequest{RootID: p.Roots[0].ID, Path: "a.txt", Content: "in app", Encoding: textfile.UTF8, BaseSHA256: textfile.SHA256([]byte("before"))})
	testutil.FailErr(t, "plan pending write", err)
	now := time.Now().UTC()
	row := &sourceMutationRow{ID: uuid.NewString(), ProjectID: p.ID, Kind: "write", InputDigest: "input", Status: status, CreatedAt: now, UpdatedAt: now,
		Plan: sourceMutationPlan{
			sourceMutationAttribution: sourceMutationAttribution{ProjectID: p.ID, WorkspaceID: p.WorkspaceID()},
			Kind:                      "write",
			RootID:                    p.Roots[0].ID,
			RootPath:                  root,
			Path:                      "a.txt",
			AbsPath:                   write.Result.AbsPath,
			Before:                    write.Result.Before,
			After:                     write.Result.After,
			BaseSHA256:                write.BaseSHA256,
			AfterSHA:                  write.Result.SHA256,
			Changed:                   true,
			Response:                  json.RawMessage(`{}`),
		}}
	testutil.FailErr(t, "persist pending write", service.Journal.insert(t.Context(), row))
	testutil.FailErr(t, "publish before attribution", applyProjectSourceWrite(write))
	return service, p, row
}

func observeMutationPaths(t *testing.T, service *SourceMutationService, p *Project, paths ...string) int {
	t.Helper()
	refs := make([]sourceledger.PathRef, 0, len(paths))
	for _, path := range paths {
		refs = append(refs, sourceledger.PathRef{RootID: p.Roots[0].ID, Path: path})
	}
	count, err := service.settlement.recorder.(*sourceledger.Store).Inventory.ObservePaths(t.Context(), p.ID, []sourceledger.RootSpec{{ID: p.Roots[0].ID, Path: p.Roots[0].Path}}, refs)
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
			testutil.FailErr(t, "commit pending attribution", service.settlement.commit(t.Context(), row))
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
	pending, err := p.service.Journal.ObservationScope(ctx, tx, projectID)
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
	service.settlement.recorder.(*sourceledger.Store).SetMutationScopeProvider(scope)
	type result struct {
		count int
		err   error
	}
	finished := make(chan result, 1)
	go func() {
		count, err := service.settlement.recorder.(*sourceledger.Store).Inventory.ObservePaths(t.Context(), p.ID, []sourceledger.RootSpec{{ID: p.Roots[0].ID, Path: p.Roots[0].Path}}, []sourceledger.PathRef{{RootID: p.Roots[0].ID, Path: "a.txt"}})
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
		commitFinished <- service.settlement.commit(t.Context(), row)
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

func TestRecoveryKeepsAdmissionIdentityAndRefusesDivergedBytes(t *testing.T) {
	for _, diverged := range []bool{false, true} {
		t.Run(fmt.Sprintf("diverged=%v", diverged), func(t *testing.T) {
			service, p, row := pendingObservationWrite(t, sourceMutationFileApplied)
			personID := row.Plan.PersonID
			if personID == "" {
				t.Fatal("admission did not capture a person")
			}
			if diverged {
				testutil.FailErr(t, "replace pending bytes", os.WriteFile(row.Plan.AbsPath, []byte("later outside edit"), 0o600))
			}
			restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
			ctx := people.WithCaller(t.Context(), people.Person{ID: uuid.NewString(), Role: api.PersonRoleOwner})
			recoveryErr := restarted.Recover(ctx)
			if diverged {
				if !errors.Is(recoveryErr, ErrSourceMutationDiverged) {
					t.Fatalf("recovery error=%v", recoveryErr)
				}
			} else {
				testutil.FailErr(t, "recover with different caller", recoveryErr)
			}
			loaded, found, err := restarted.Journal.load(t.Context(), row.ID)
			testutil.FailErr(t, "load recovered receipt", err)
			if !found || loaded.Plan.PersonID != personID {
				t.Fatal("recovery changed admission identity")
			}
			if diverged {
				if loaded.Status != sourceMutationDiverged {
					t.Fatalf("diverged status=%s", loaded.Status)
				}
				assertSourceHistoryFile(t, p.Roots[0].Path, "a.txt", "later outside edit")
			} else {
				if loaded.Status != sourceMutationCommitted {
					t.Fatalf("settlement status=%s: %s", loaded.Status, loaded.Error)
				}
				var recordedPerson string
				testutil.FailErr(t, "read attribution", service.Journal.db.QueryRowContext(t.Context(), `SELECT person_id FROM source_operations WHERE operation_key = ?`, row.ID).Scan(&recordedPerson))
				if recordedPerson != personID {
					t.Fatalf("attribution person=%s want=%s", recordedPerson, personID)
				}
			}
		})
	}
}

func TestMutationAttributionPreservesDurableFlatPlan(t *testing.T) {
	const stored = `{"project_id":"p","workspace_id":"worktree:w","branch_id":"worktree:w","session_id":"session","turn":4,"person_id":"person","batch_id":"batch","cause":"restore","kind":"write","root_id":"root","root_path":"/root","changed":true,"response":{}}`
	var plan sourceMutationPlan
	testutil.FailErr(t, "decode durable plan", json.Unmarshal([]byte(stored), &plan))
	if plan.ProjectID != "p" || plan.PersonID != "person" || plan.BranchID != sourcebranch.ForWorktree("w") {
		t.Fatal("durable admission identity changed")
	}
	encoded, err := json.Marshal(plan)
	testutil.FailErr(t, "encode durable plan", err)
	var before, after map[string]any
	testutil.FailErr(t, "decode stored keys", json.Unmarshal([]byte(stored), &before))
	testutil.FailErr(t, "decode emitted keys", json.Unmarshal(encoded, &after))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("durable plan keys changed: %s", encoded)
	}
}
