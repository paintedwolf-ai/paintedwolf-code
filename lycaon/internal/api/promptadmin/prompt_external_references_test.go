package promptadmin

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func canonPath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return resolved
}

func TestPromptAdmissionWithExternalReferencesGrantsAccessAndPerceivesImages(t *testing.T) {
	handler, _, attachments, sess := newPromptAttachmentRecoveryFixture(t)
	grantedRT := grantedpath.NewRuntime()
	handler.Submission.GrantedPaths = grantedRT
	handler.Submission.VisualStore = visual.NewMemoryStore()
	handler.Submission.Caps = promptattach.Active()
	handler.Submission.References = &References{Caps: promptattach.Active()}

	tmp := canonPath(t, t.TempDir())
	extFile := filepath.Join(tmp, "dropped.txt")
	testutil.FailErr(t, "write file", os.WriteFile(extFile, []byte("external notes"), 0o644))

	extDir := filepath.Join(tmp, "dropped-dir")
	testutil.FailErr(t, "mkdir", os.Mkdir(extDir, 0o755))

	extImg := filepath.Join(tmp, "dropped.png")
	testutil.FailErr(t, "write img", os.WriteFile(extImg, visual.TestPNG1x1Bytes(), 0o644))

	req := wire.PromptRequest{
		OperationID: uuid.NewString(),
		Text:        "Check these files",
		References: []wire.PromptReferencePart{
			wire.NewPromptReferenceExternalFile(wire.PromptReferenceExternalFilePart{
				ProjectID: sess.ProjectID,
				Path:      extFile,
			}),
			wire.NewPromptReferenceExternalFolder(wire.PromptReferenceExternalFolderPart{
				ProjectID: sess.ProjectID,
				Path:      extDir,
			}),
			wire.NewPromptReferenceExternalFile(wire.PromptReferenceExternalFilePart{
				ProjectID: sess.ProjectID,
				Path:      extImg,
			}),
		},
	}

	response := httptest.NewRecorder()
	prepared, ok := handler.Submission.preparePromptAdmission(
		response, httptest.NewRequest("POST", "/", nil),
		sess.ID, sess, req, attachments, req.Text, req.Text, false,
	)
	if !ok {
		t.Fatalf("preparePromptAdmission failed: code=%d body=%s", response.Code, response.Body.String())
	}

	if len(grantedRT.List(sess.ID)) != 0 {
		t.Fatal("preparing a prompt installed access before admission")
	}
	installed, err := handler.Submission.grantPromptReferences(t.Context(), sess.ID, req.OperationID, prepared.input.ContentParts)
	testutil.FailErr(t, "install submission grants", err)
	defer handler.Submission.revokePromptGrants(installed)

	// Verify grants in GrantedPaths
	if _, ok := grantedRT.Covers(sess.ID, "", extFile, grantedpath.ModeWrite); !ok {
		t.Errorf("file %s was not granted write mode", extFile)
	}
	if _, ok := grantedRT.Covers(sess.ID, "", extDir, grantedpath.ModeWrite); !ok {
		t.Errorf("folder %s was not granted write mode", extDir)
	}
	// Verify directory tree coverage
	childPath := filepath.Join(extDir, "child.txt")
	if _, ok := grantedRT.Covers(sess.ID, "", childPath, grantedpath.ModeWrite); !ok {
		t.Errorf("child %s under folder grant was not covered", childPath)
	}

	// Verify image artifact perception
	if len(prepared.input.ArtifactIDs) != 1 {
		t.Fatalf("expected 1 image artifact ID, got %v", prepared.input.ArtifactIDs)
	}
	artID := prepared.input.ArtifactIDs[0]
	res := handler.Submission.VisualStore.Resolve(t.Context(), sess.ID, artID)
	if !res.IsPresent() {
		t.Fatalf("visual artifact not present: %s", res.Note())
	}
	if res.Meta().Mime != "image/png" {
		t.Fatalf("visual mime = %q want image/png", res.Meta().Mime)
	}

	linked := false
	for _, part := range prepared.input.ContentParts {
		if part.Path == extImg && part.ArtifactID == artID {
			linked = true
		}
	}
	if !linked {
		t.Fatal("image artifact is not bound to its source reference")
	}

	// Verify content parts contains references
	if len(prepared.input.ContentParts) == 0 {
		t.Fatalf("expected ContentParts to be populated")
	}
}

func TestPromptGrantRollbackPreservesAnotherSubmission(t *testing.T) {
	handler, _, _, sess := newPromptAttachmentRecoveryFixture(t)
	handler.Submission.Store = handler.Attachments.Store
	handler.Submission.GrantedPaths = grantedpath.NewRuntime()
	path := filepath.Join(canonPath(t, t.TempDir()), "notes.txt")
	parts := []wire.MessageContentPart{{Path: path, ReferenceKind: wire.MessageReferenceKindExternalFile}}
	first, err := handler.Submission.grantPromptReferences(t.Context(), sess.ID, uuid.NewString(), parts)
	testutil.FailErr(t, "first grant", err)
	second, err := handler.Submission.grantPromptReferences(t.Context(), sess.ID, uuid.NewString(), parts)
	testutil.FailErr(t, "second grant", err)
	handler.Submission.revokePromptGrants(second)
	if _, ok := handler.Submission.GrantedPaths.Covers(sess.ID, "", path, grantedpath.ModeWrite); !ok {
		t.Fatal("rollback revoked another submission's grant")
	}
	handler.Submission.revokePromptGrants(first)
	if _, ok := handler.Submission.GrantedPaths.Covers(sess.ID, "", path, grantedpath.ModeWrite); ok {
		t.Fatal("refused submission retained access")
	}

	if _, err := handler.Submission.grantPromptReferences(t.Context(), "", uuid.NewString(), []wire.MessageContentPart{
		{Path: path, ReferenceKind: wire.MessageReferenceKindExternalFile},
		{Path: "relative.txt", ReferenceKind: wire.MessageReferenceKindExternalFile},
	}); err == nil {
		t.Fatal("expected relative external reference grant to fail")
	}
	handler.Submission.GrantedPaths = nil
	if _, err := handler.Submission.grantPromptReferences(t.Context(), sess.ID, uuid.NewString(), parts); err == nil {
		t.Fatal("expected nil GrantedPaths to refuse external reference grants")
	}
}

func TestReadReferenceImageValidatesProjectRootFile(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	var deps apitestdeps.Deps
	apitestdeps.Fill(t, &deps)
	rootDir := canonPath(t, t.TempDir())
	p, err := project.CreateWithRoot(t.Context(), deps.Projects, rootDir)
	testutil.FailErr(t, "create project", err)
	rootID := p.Roots[0].ID

	testutil.FailErr(t, "write png", os.WriteFile(filepath.Join(rootDir, "pic.png"), visual.TestPNG1x1Bytes(), 0o644))
	testutil.FailErr(t, "write text", os.WriteFile(filepath.Join(rootDir, "notes.txt"), []byte("hello"), 0o644))

	refs := &References{Caps: promptattach.Active(), Projects: deps.Projects}
	refDeps := refs.ReferenceDeps(t.Context(), &wire.Session{ID: "sess-1", ProjectID: p.ID})
	raw, mime, err := refDeps.ReadImage(p.ID, rootID, "pic.png")
	testutil.FailErr(t, "read reference image", err)
	if mime != "image/png" || len(raw) == 0 {
		t.Fatalf("got mime=%q len=%d, want image/png", mime, len(raw))
	}
	if _, _, err := refDeps.ReadImage("missing-project", rootID, "pic.png"); err == nil {
		t.Fatal("expected missing project error")
	}
	if _, _, err := refDeps.ReadImage(p.ID, rootID, "missing.png"); err == nil {
		t.Fatal("expected missing file error")
	}
	if _, _, err := refDeps.ReadImage(p.ID, rootID, "notes.txt"); err == nil {
		t.Fatal("expected non-image file error")
	}
}
