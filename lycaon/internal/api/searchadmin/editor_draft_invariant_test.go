package searchadmin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"log/slog"
)

type draftSearchFixture struct {
	*searchFixture
	editors *editordoc.Service
	owner   people.Person
}

func newDraftSearchFixture(t *testing.T) *draftSearchFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	projects := project.NewSQLRegistry(database)
	ledger := sourceledger.New(database, t.TempDir(), sourceledger.Timing{})
	editors := editordoc.New(editordoc.NewStore(database), ledger, ledger.History, projects)
	t.Cleanup(func() { _ = editors.Close(context.Background()) })

	responses := &httpio.Responder{Logger: slog.New(slog.DiscardHandler)}
	f := &searchFixture{database: database, projects: projects}
	f.handler = New(responses, Dependencies{
		Database:        database,
		Projects:        projects,
		EditorDocuments: editors,
		SourceMutations: projectsource.NewSourceMutationService(database, ledger),
		ChatAffiliation: func(*http.Request) (string, int) { return "", 0 },
		WriteSourceError: func(w http.ResponseWriter, _ *http.Request, err error) {
			f.sourceErrors = append(f.sourceErrors, err)
			responses.Fail(w, wire.ApiErrorCodeSourceWriteConflict, "source mutation refused")
		},
	})
	f.router = f.mount(f.handler)
	return &draftSearchFixture{
		searchFixture: f,
		editors:       editors,
		owner:         testutil.HostOwner(),
	}
}

func (f *draftSearchFixture) postAs(t *testing.T, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *draftSearchFixture) searchCode(t *testing.T, ctx context.Context, projectID, query string) wire.SearchResponse {
	t.Helper()
	rec := f.postAs(t, ctx, "/v1/search", jsonBody(t, wire.SearchRequest{
		Query:           query,
		OriginProjectID: projectID,
		Budget:          wire.SearchBudgetComplete,
	}))
	return decodeOK[wire.SearchResponse](t, rec)
}

func TestSearchCodeOverlaysAuthorizedEditorDraftsAndRespectsLifecycleAndIsolation(t *testing.T) {
	f := newDraftSearchFixture(t)
	ownerCtx := people.WithCaller(t.Context(), f.owner)
	guestCtx := people.WithCaller(t.Context(), people.Person{ID: "person-guest", Role: wire.PersonRole("guest")})

	// Project A with two roots (primary root + secondary root) and Project B.
	dirA1 := t.TempDir()
	dirA2 := t.TempDir()
	draftDiskBytes := []byte("disk_only_original_s02\n")
	testutil.FailErr(t, "write draft_target.txt", os.WriteFile(filepath.Join(dirA1, "draft_target.txt"), draftDiskBytes, 0o600))
	testutil.FailErr(t, "write saved_control.txt", os.WriteFile(filepath.Join(dirA1, "saved_control.txt"), []byte("saved_s02_positive_control\n"), 0o600))
	testutil.FailErr(t, "write root2_file.txt", os.WriteFile(filepath.Join(dirA2, "root2_file.txt"), []byte("root2_saved_token\n"), 0o600))

	projA, err := f.projects.Create(t.Context(), project.CreateParams{
		Name: "proj-a",
		Roots: []project.AttachRootParams{
			{Path: dirA1, Label: "root-a1"},
			{Path: dirA2, Label: "root-a2"},
		},
	})
	testutil.FailErr(t, "create projA", err)
	for _, r := range projA.Roots {
		testutil.FailErr(t, "settle projA root", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), projA.ID, sourcecatalog.Root{ID: r.ID, Path: r.Path}))
	}
	projB := f.addProject(t, "proj-b", "root-b", map[string]string{
		"other.txt": "proj_b_saved_token\n",
	})

	// Open and dirty draft_target.txt in Project A, root 0 (disk stays unchanged).
	doc, err := f.editors.Open(t.Context(), projA, "draft_target.txt", projA.Roots[0].ID, "", "window-1", nil)
	testutil.FailErr(t, "open draft_target.txt", err)
	doc, err = f.editors.ReplaceSnapshot(t.Context(), doc.ID, projA.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: doc.Revision,
		},
		Content: "unsaved_draft_s02_token\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "dirty draft_target.txt", err)
	if !doc.Dirty {
		t.Fatal("expected draft_target.txt to be dirty")
	}

	// 1. Authorized owner finds dirty draft token AND does NOT see stale disk token.
	draftResp := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code unsaved_draft_s02_token")
	if len(draftResp.Hits) != 1 || draftResp.Hits[0].Path != "draft_target.txt" || draftResp.Hits[0].RootID != projA.Roots[0].ID {
		t.Fatalf("authorized draft search = %+v, want 1 hit at draft_target.txt", draftResp.Hits)
	}
	staleResp := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code disk_only_original_s02")
	if len(staleResp.Hits) != 0 {
		t.Fatalf("stale disk token leaked through dirty draft: %+v", staleResp.Hits)
	}
	// Disk bytes remain untouched.
	afterDisk, err := os.ReadFile(filepath.Join(dirA1, "draft_target.txt"))
	testutil.FailErr(t, "read disk draft_target.txt", err)
	if string(afterDisk) != string(draftDiskBytes) {
		t.Fatalf("disk bytes mutated: got %q, want %q", string(afterDisk), string(draftDiskBytes))
	}

	// 2. Positive saved control in same project & root.
	savedResp := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code saved_s02_positive_control")
	if len(savedResp.Hits) != 1 || savedResp.Hits[0].Path != "saved_control.txt" {
		t.Fatalf("saved control = %+v, want 1 hit at saved_control.txt", savedResp.Hits)
	}

	// 3. New dirty path (not in settled catalog generation & absent on disk) is
	// included within the code-search boundary and respects path filters.
	newPath := filepath.Join(dirA1, "new_unsaved_draft.txt")
	testutil.FailErr(t, "seed new_unsaved_draft.txt", os.WriteFile(newPath, []byte("seed\n"), 0o600))
	absentDoc, err := f.editors.Open(t.Context(), projA, "new_unsaved_draft.txt", projA.Roots[0].ID, "", "window-1", nil)
	testutil.FailErr(t, "open new_unsaved_draft.txt", err)
	testutil.FailErr(t, "remove new_unsaved_draft.txt from disk", os.Remove(newPath))
	absentDoc, err = f.editors.ReplaceSnapshot(t.Context(), absentDoc.ID, projA.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: absentDoc.Revision,
		},
		Content: "brand_new_absent_draft_token\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "dirty absent draft", err)
	newDraftResp := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code brand_new_absent_draft_token")
	if len(newDraftResp.Hits) != 1 || newDraftResp.Hits[0].Path != "new_unsaved_draft.txt" {
		t.Fatalf("new absent draft search = %+v, want 1 hit at new_unsaved_draft.txt", newDraftResp.Hits)
	}
	// Respects path filter boundary.
	filteredNewResp := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code path:saved_control.txt brand_new_absent_draft_token")
	if len(filteredNewResp.Hits) != 0 {
		t.Fatalf("absent draft bypassed path filter: %+v", filteredNewResp.Hits)
	}

	// 4. Negative controls: missing caller, non-owner caller, other project, other root, export.
	noCallerResp := f.searchCode(t, t.Context(), projA.ID, "project:current kind:code unsaved_draft_s02_token")
	if len(noCallerResp.Hits) != 0 {
		t.Fatalf("unauthenticated caller saw dirty draft: %+v", noCallerResp.Hits)
	}
	guestResp := f.searchCode(t, guestCtx, projA.ID, "project:current kind:code unsaved_draft_s02_token")
	if len(guestResp.Hits) != 0 {
		t.Fatalf("guest caller saw dirty draft: %+v", guestResp.Hits)
	}
	otherProjResp := f.searchCode(t, ownerCtx, projB.ID, "project:current kind:code unsaved_draft_s02_token")
	if len(otherProjResp.Hits) != 0 {
		t.Fatalf("project B saw project A dirty draft: %+v", otherProjResp.Hits)
	}
	otherRootResp := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code path:root2_file.txt unsaved_draft_s02_token")
	if len(otherRootResp.Hits) != 0 {
		t.Fatalf("root 2 path filter saw root 1 dirty draft: %+v", otherRootResp.Hits)
	}
	// Export must NOT leak unsaved draft bytes.
	exportRec := f.postAs(t, ownerCtx, "/v1/search/export", jsonBody(t, wire.SearchExportRequest{
		Query:           "project:current kind:code unsaved_draft_s02_token",
		OriginProjectID: projA.ID,
		Format:          "jsonl",
	}))
	if exportRec.Code != http.StatusOK {
		t.Fatalf("export status = %d body=%s", exportRec.Code, exportRec.Body.String())
	}
	if strings.Contains(exportRec.Body.String(), "unsaved_draft_s02_token") {
		t.Fatalf("search export leaked unsaved draft bytes: %s", exportRec.Body.String())
	}

	// 5. Negative control: worker-branch dirty draft must NOT leak into trunk search.
	workerBranch, err := sourcebranch.ForWorker("worker-isolated-1")
	testutil.FailErr(t, "worker branch", err)
	workerProj := *projA
	workerProj.SourceBranch = workerBranch
	workerDoc, err := f.editors.Open(t.Context(), &workerProj, "saved_control.txt", projA.Roots[0].ID, "", "worker-client", nil)
	testutil.FailErr(t, "open worker draft", err)
	_, err = f.editors.ReplaceSnapshot(t.Context(), workerDoc.ID, projA.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "worker-client",
			OperationID:      uuid.NewString(),
			ExpectedRevision: workerDoc.Revision,
		},
		Content: "worker_only_isolated_draft_token\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "dirty worker draft", err)
	workerLeakResp := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code worker_only_isolated_draft_token")
	if len(workerLeakResp.Hits) != 0 {
		t.Fatalf("worker-branch dirty draft leaked into trunk search: %+v", workerLeakResp.Hits)
	}

	// 6. Lifecycle: Discard reverts search to disk; Save persists to disk and clears dirty overlay.
	doc, err = f.editors.Discard(t.Context(), doc.ID, projA.ID, editordoc.DocumentCommand{
		ClientID:         "window-1",
		OperationID:      uuid.NewString(),
		ExpectedRevision: doc.Revision,
	})
	testutil.FailErr(t, "discard draft", err)
	afterDiscardDraft := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code unsaved_draft_s02_token")
	if len(afterDiscardDraft.Hits) != 0 {
		t.Fatalf("discarded draft token still matched: %+v", afterDiscardDraft.Hits)
	}
	afterDiscardDisk := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code disk_only_original_s02")
	if len(afterDiscardDisk.Hits) != 1 || afterDiscardDisk.Hits[0].Path != "draft_target.txt" {
		t.Fatalf("discard did not restore disk visibility: %+v", afterDiscardDisk.Hits)
	}

	// Re-edit and save: search finds saved token and disk holds it.
	doc, err = f.editors.ReplaceSnapshot(t.Context(), doc.ID, projA.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: doc.Revision,
		},
		Content: "saved_after_edit_s02_token\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "re-dirty before save", err)
	savedDoc, err := f.editors.Save(t.Context(), projA, doc.ID, "window-1", uuid.NewString(), "", 0, doc.Revision)
	testutil.FailErr(t, "save draft", err)
	if savedDoc.Dirty {
		t.Fatal("expected saved document to be clean")
	}
	afterSaveResp := f.searchCode(t, ownerCtx, projA.ID, "project:current kind:code saved_after_edit_s02_token")
	if len(afterSaveResp.Hits) != 1 || afterSaveResp.Hits[0].Path != "draft_target.txt" {
		t.Fatalf("saved token not found after save: %+v", afterSaveResp.Hits)
	}
}
