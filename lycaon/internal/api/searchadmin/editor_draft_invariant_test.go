package searchadmin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type stubSearchSessions struct {
	sessions map[string]*wire.Session
	bindings map[string]*store.WorktreeBinding
}

func (s *stubSearchSessions) Get(_ context.Context, id string) (*wire.Session, error) {
	if sess, ok := s.sessions[id]; ok {
		return sess, nil
	}
	return nil, store.ErrSessionNotFound
}

func (s *stubSearchSessions) GetWorktreeBinding(_ context.Context, id string) (*store.WorktreeBinding, bool, error) {
	b, ok := s.bindings[id]
	return b, ok, nil
}

func (s *stubSearchSessions) ResolveEffectiveMode(context.Context, string) (store.RootAuthority, error) {
	return store.RootAuthority{}, nil
}

type draftSearchFixture struct {
	*searchFixture
	editors  *editordoc.Service
	sessions *stubSearchSessions
	owner    people.Person
}

func newDraftSearchFixture(t *testing.T) *draftSearchFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	projects := project.NewSQLRegistry(database)
	ledger := sourceledger.New(database, t.TempDir(), sourceledger.Timing{})
	editors := editordoc.New(editordoc.NewStore(database), ledger, ledger.History, projects)
	t.Cleanup(func() { _ = editors.Close(context.Background()) })

	sessions := &stubSearchSessions{
		sessions: map[string]*wire.Session{},
		bindings: map[string]*store.WorktreeBinding{},
	}
	responses := &httpio.Responder{Logger: slog.New(slog.DiscardHandler)}
	f := &searchFixture{database: database, projects: projects}
	f.handler = New(responses, Dependencies{
		Database:        database,
		Projects:        projects,
		SessionStore:    sessions,
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
		sessions:      sessions,
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

func (f *draftSearchFixture) searchCode(t *testing.T, ctx context.Context, endpoint, projectID, query string, budget wire.SearchBudget) wire.SearchResponse {
	t.Helper()
	rec := f.postAs(t, ctx, endpoint, jsonBody(t, wire.SearchRequest{
		Query:           query,
		OriginProjectID: projectID,
		Budget:          budget,
	}))
	return decodeOK[wire.SearchResponse](t, rec)
}

func TestSearchCodeOverlaysAuthorizedEditorDraftsAndRespectsLifecycleAndIsolation(t *testing.T) {
	f := newDraftSearchFixture(t)
	ownerCtx := people.WithCaller(t.Context(), f.owner)
	guestCtx := people.WithCaller(t.Context(), people.Person{ID: "person-guest", Role: wire.PersonRole("guest")})

	// Project A with two roots (primary + secondary) and Project B.
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
	// Register a session belonging to Project B (another workspace) and a trunk session for Project A.
	f.sessions.sessions["sess-proj-a-trunk"] = &wire.Session{ID: "sess-proj-a-trunk", ProjectID: projA.ID}
	f.sessions.sessions["sess-other-workspace"] = &wire.Session{ID: "sess-other-workspace", ProjectID: projB.ID}

	// Open and dirty draft_target.txt in Project A, root 0 (disk stays unchanged).
	doc, err := f.editors.Open(t.Context(), projA, "draft_target.txt", projA.Roots[0].ID, "", "window-1", nil)
	testutil.FailErr(t, "open draft_target.txt", err)
	doc, err = f.editors.ReplaceSnapshot(t.Context(), doc.ID, projA.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: doc.Revision,
		},
		Content: "unsaved_draft_s02_token line_one\nunsaved_draft_s02_token line_two\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "dirty draft_target.txt", err)
	if !doc.Dirty {
		t.Fatal("expected draft_target.txt to be dirty")
	}

	// 1. Authorized owner with DEFAULT interactive Den search budget finds dirty
	// draft hits, paginates across pages, and sees 0 hits for overwritten disk text.
	page1Req := wire.SearchRequest{
		Query:           "project:current kind:code unsaved_draft_s02_token",
		OriginProjectID: projA.ID,
		Limit:           1,
		// Omitted budget exercises the default interactive Den budget.
	}
	page1 := decodeOK[wire.SearchResponse](t, f.postAs(t, ownerCtx, "/v1/search?session_id=sess-proj-a-trunk", jsonBody(t, page1Req)))
	if len(page1.Hits) != 1 || page1.Hits[0].Path != "draft_target.txt" || page1.NextCursor == "" {
		t.Fatalf("interactive page 1 = %+v, want 1 hit at draft_target.txt + next_cursor", page1)
	}
	page2Req := page1Req
	page2Req.Cursor = page1.NextCursor
	page2 := decodeOK[wire.SearchResponse](t, f.postAs(t, ownerCtx, "/v1/search?session_id=sess-proj-a-trunk", jsonBody(t, page2Req)))
	if len(page2.Hits) != 1 || page2.Hits[0].Path != "draft_target.txt" || page2.Hits[0].Line == page1.Hits[0].Line {
		t.Fatalf("interactive page 2 = %+v, want distinct second draft line hit", page2)
	}

	// Cross-workspace and unauthorized callers MUST NOT replay page1.NextCursor.
	requireErrorCode(t, f.postAs(t, ownerCtx, "/v1/search?session_id=sess-other-workspace", jsonBody(t, page2Req)), wire.ApiErrorCodeInvalidPageCursor)
	requireErrorCode(t, f.postAs(t, guestCtx, "/v1/search?session_id=sess-proj-a-trunk", jsonBody(t, page2Req)), wire.ApiErrorCodeInvalidPageCursor)
	requireErrorCode(t, f.postAs(t, t.Context(), "/v1/search?session_id=sess-proj-a-trunk", jsonBody(t, page2Req)), wire.ApiErrorCodeInvalidPageCursor)

	// Authorized person acting in ANOTHER workspace sees 0 trunk draft hits.
	otherWsResp := f.searchCode(t, ownerCtx, "/v1/search?session_id=sess-other-workspace", projA.ID, "project:current kind:code unsaved_draft_s02_token", wire.SearchBudgetInteractive)
	if len(otherWsResp.Hits) != 0 {
		t.Fatalf("authorized person in another workspace saw trunk dirty draft: %+v", otherWsResp.Hits)
	}

	staleResp := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code disk_only_original_s02", wire.SearchBudgetInteractive)
	if len(staleResp.Hits) != 0 {
		t.Fatalf("stale disk token leaked through dirty draft: %+v", staleResp.Hits)
	}
	afterDisk, err := os.ReadFile(filepath.Join(dirA1, "draft_target.txt"))
	testutil.FailErr(t, "read disk draft_target.txt", err)
	if string(afterDisk) != string(draftDiskBytes) {
		t.Fatalf("disk bytes mutated: got %q, want %q", string(afterDisk), string(draftDiskBytes))
	}

	// 2. Positive saved control in same project & root.
	savedResp := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code saved_s02_positive_control", wire.SearchBudgetInteractive)
	if len(savedResp.Hits) != 1 || savedResp.Hits[0].Path != "saved_control.txt" {
		t.Fatalf("saved control = %+v, want 1 hit at saved_control.txt", savedResp.Hits)
	}

	// 3. New dirty path (absent from catalog & disk) included within code-search boundary.
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
	newDraftResp := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code brand_new_absent_draft_token", wire.SearchBudgetComplete)
	if len(newDraftResp.Hits) != 1 || newDraftResp.Hits[0].Path != "new_unsaved_draft.txt" {
		t.Fatalf("new absent draft search = %+v, want 1 hit at new_unsaved_draft.txt", newDraftResp.Hits)
	}
	filteredNewResp := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code path:saved_control.txt brand_new_absent_draft_token", wire.SearchBudgetComplete)
	if len(filteredNewResp.Hits) != 0 {
		t.Fatalf("absent draft bypassed path filter: %+v", filteredNewResp.Hits)
	}

	// 4. Negative controls: missing caller, non-owner caller, other project, other root, export.
	noCallerResp := f.searchCode(t, t.Context(), "/v1/search", projA.ID, "project:current kind:code unsaved_draft_s02_token", wire.SearchBudgetComplete)
	if len(noCallerResp.Hits) != 0 {
		t.Fatalf("unauthenticated caller saw dirty draft: %+v", noCallerResp.Hits)
	}
	guestResp := f.searchCode(t, guestCtx, "/v1/search", projA.ID, "project:current kind:code unsaved_draft_s02_token", wire.SearchBudgetComplete)
	if len(guestResp.Hits) != 0 {
		t.Fatalf("guest caller saw dirty draft: %+v", guestResp.Hits)
	}
	otherProjResp := f.searchCode(t, ownerCtx, "/v1/search", projB.ID, "project:current kind:code unsaved_draft_s02_token", wire.SearchBudgetComplete)
	if len(otherProjResp.Hits) != 0 {
		t.Fatalf("project B saw project A dirty draft: %+v", otherProjResp.Hits)
	}
	otherRootResp := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code path:root2_file.txt unsaved_draft_s02_token", wire.SearchBudgetComplete)
	if len(otherRootResp.Hits) != 0 {
		t.Fatalf("root 2 path filter saw root 1 dirty draft: %+v", otherRootResp.Hits)
	}
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
	workerLeakResp := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code worker_only_isolated_draft_token", wire.SearchBudgetComplete)
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
	afterDiscardDraft := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code unsaved_draft_s02_token", wire.SearchBudgetComplete)
	if len(afterDiscardDraft.Hits) != 0 {
		t.Fatalf("discarded draft token still matched: %+v", afterDiscardDraft.Hits)
	}
	afterDiscardDisk := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code disk_only_original_s02", wire.SearchBudgetComplete)
	if len(afterDiscardDisk.Hits) != 1 || afterDiscardDisk.Hits[0].Path != "draft_target.txt" {
		t.Fatalf("discard did not restore disk visibility: %+v", afterDiscardDisk.Hits)
	}
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
	afterSaveResp := f.searchCode(t, ownerCtx, "/v1/search", projA.ID, "project:current kind:code saved_after_edit_s02_token", wire.SearchBudgetComplete)
	if len(afterSaveResp.Hits) != 1 || afterSaveResp.Hits[0].Path != "draft_target.txt" {
		t.Fatalf("saved token not found after save: %+v", afterSaveResp.Hits)
	}
}

func TestSearchCodeDraftRespectsHiddenAndDependencyExclusionBoundaries(t *testing.T) {
	f := newDraftSearchFixture(t)
	ownerCtx := people.WithCaller(t.Context(), f.owner)

	dir := t.TempDir()
	testutil.FailErr(t, "write visible file", os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("visible_saved_control\n"), 0o600))
	testutil.FailErr(t, "write hidden file", os.WriteFile(filepath.Join(dir, ".hidden_draft.txt"), []byte("hidden_disk_seed\n"), 0o600))
	testutil.FailErr(t, "mkdir node_modules", os.MkdirAll(filepath.Join(dir, "node_modules"), 0o700))
	testutil.FailErr(t, "write node_modules file", os.WriteFile(filepath.Join(dir, "node_modules", "dep.txt"), []byte("dep_disk_seed\n"), 0o600))

	proj, err := f.projects.Create(t.Context(), project.CreateParams{
		Name:  "proj-exclusions",
		Roots: []project.AttachRootParams{{Path: dir, Label: "root-excl"}},
	})
	testutil.FailErr(t, "create proj", err)
	testutil.FailErr(t, "settle index", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), proj.ID, sourcecatalog.Root{ID: proj.Roots[0].ID, Path: proj.Roots[0].Path}))

	// Dirty hidden file (no positive path filter) and node_modules file MUST stay excluded.
	hidDoc, err := f.editors.Open(t.Context(), proj, ".hidden_draft.txt", proj.Roots[0].ID, "", "window-1", nil)
	testutil.FailErr(t, "open hidden file", err)
	_, err = f.editors.ReplaceSnapshot(t.Context(), hidDoc.ID, proj.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: hidDoc.Revision,
		},
		Content: "hidden_dirty_secret_token\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "dirty hidden file", err)
	hidResp := f.searchCode(t, ownerCtx, "/v1/search", proj.ID, "project:current kind:code hidden_dirty_secret_token", wire.SearchBudgetComplete)
	if len(hidResp.Hits) != 0 {
		t.Fatalf("excluded hidden draft leaked into default code search: %+v", hidResp.Hits)
	}

	depDoc, err := f.editors.Open(t.Context(), proj, "node_modules/dep.txt", proj.Roots[0].ID, "", "window-1", nil)
	testutil.FailErr(t, "open dep file", err)
	_, err = f.editors.ReplaceSnapshot(t.Context(), depDoc.ID, proj.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: depDoc.Revision,
		},
		Content: "dep_dirty_excluded_token\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "dirty dep file", err)
	depResp := f.searchCode(t, ownerCtx, "/v1/search", proj.ID, "project:current kind:code dep_dirty_excluded_token", wire.SearchBudgetComplete)
	if len(depResp.Hits) != 0 {
		t.Fatalf("excluded dependency draft leaked into default code search: %+v", depResp.Hits)
	}
}

func TestSearchCodeDraftCompletesWhileWriterTransactionIsHeld(t *testing.T) {
	f := newDraftSearchFixture(t)
	ownerCtx, cancel := context.WithTimeout(people.WithCaller(t.Context(), f.owner), 5*time.Second)
	defer cancel()

	proj := f.addProject(t, "proj-reader-pool", "root-rp", map[string]string{
		"draft_target.txt": "disk_seed_rp\n",
	})
	doc, err := f.editors.Open(t.Context(), proj, "draft_target.txt", proj.Roots[0].ID, "", "window-1", nil)
	testutil.FailErr(t, "open draft_target.txt", err)
	_, err = f.editors.ReplaceSnapshot(t.Context(), doc.ID, proj.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: doc.Revision,
		},
		Content: "writer_held_draft_token\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "dirty draft_target.txt", err)

	// Hold the single durable writer transaction open with an active write lock.
	wtx, err := f.database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin writer tx", err)
	defer func() { _ = wtx.Rollback() }()
	_, err = wtx.ExecContext(t.Context(), `INSERT OR REPLACE INTO store_meta(key, value) VALUES ('cf06_writer_lock', 'held')`)
	testutil.FailErr(t, "acquire writer lock", err)

	// Draft-enabled search MUST use BeginReadTx on the concurrent reader pool
	// and complete immediately without blocking on the held writer transaction.
	resp := f.searchCode(t, ownerCtx, "/v1/search", proj.ID, "project:current kind:code writer_held_draft_token", wire.SearchBudgetInteractive)
	if len(resp.Hits) != 1 || resp.Hits[0].Path != "draft_target.txt" {
		t.Fatalf("search while writer tx held = %+v, want 1 hit at draft_target.txt", resp.Hits)
	}
}
