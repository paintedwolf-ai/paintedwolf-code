package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRestoreVersionEnforcesConversationalReadBasisAndPreservesPositivePath(t *testing.T) {
	dir := t.TempDir()
	v1Content := "package main\n\nfunc main() {}\n"
	v2Content := "package main\n\nfunc broken() {}\n"
	writeProvenanceFile(t, dir, "main.go", v2Content)

	ledger := &fakeSourceLedger{
		heads: map[string]sourceledger.BranchHead{
			"r1/main.go": {FileID: "f1", VersionID: "v2", State: "content"},
		},
		versions: map[string]sourceledger.RestorableVersion{
			"v1": {ID: "v1", FileID: "f1", ProjectID: "p1", RootID: "r1", Path: "main.go", State: "content", Content: []byte(v1Content)},
			"v2": {ID: "v2", FileID: "f1", ProjectID: "p1", RootID: "r1", Path: "main.go", State: "content", Content: []byte(v2Content)},
		},
	}

	docs := &fakeEditorDocuments{
		docs: map[string]*tools.EditorDocumentText{
			"r1/main.go": {ID: "doc-main", Path: "main.go", Revision: 1, Text: v2Content, ReadWhole: true},
		},
	}
	tool := &RestoreVersionTool{Boundary: nativefixture.Boundary(t)}
	tctx := provenanceCtx(dir, ledger)
	tctx.Identity.ProjectID = "p1"
	tctx.Identity.SessionID = "chat-restore"
	tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindProject
	tctx.Source.EditorDocuments = docs

	// 1. Without a qualifying read, restore_version MUST refuse with EDITOR_DOCUMENT_READ_REQUIRED
	// and leave disk untouched.
	v2SHA := sha256.Sum256([]byte(v2Content))
	_, err := tool.Run(context.Background(), map[string]any{
		"path":       "main.go",
		"version_id": "v1",
	}, tctx)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "EDITOR_DOCUMENT_READ_REQUIRED" {
		t.Fatalf("restore_version without read = %v, want EDITOR_DOCUMENT_READ_REQUIRED", err)
	}
	diskBytes, readErr := os.ReadFile(filepath.Join(dir, "main.go"))
	testutil.FailErr(t, "read disk after refusal", readErr)
	if got := sha256.Sum256(diskBytes); got != v2SHA {
		t.Fatalf("refused restore_version mutated disk: got=%x want=%x", got, v2SHA)
	}

	// 2. Partial span read does NOT qualify for restore_version.
	doc, ok, openErr := docs.OpenDocument(context.Background(), "p1", sourcebranch.Trunk, "r1", "main.go")
	testutil.FailErr(t, "open doc", openErr)
	if !ok {
		t.Fatal("expected document")
	}
	docs.RememberAgentReadSpans("p1", "chat-restore", doc, []documentcore.LineSpan{{StartLine: 1, EndLine: 1}})
	_, err = tool.Run(context.Background(), map[string]any{
		"path":       "main.go",
		"version_id": "v1",
	}, tctx)
	reject = toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "EDITOR_DOCUMENT_READ_REQUIRED" {
		t.Fatalf("restore_version with partial read = %v, want EDITOR_DOCUMENT_READ_REQUIRED", err)
	}

	// 3. Full read authorizes restore_version and restores exact v1 bytes.
	docs.RememberAgentRead("p1", "chat-restore", doc)
	out, err := tool.Run(context.Background(), map[string]any{
		"path":       "main.go",
		"version_id": "v1",
	}, tctx)
	testutil.FailErr(t, "restore_version positive path", err)
	if !strings.Contains(out, "Restored main.go to version v1") {
		t.Fatalf("unexpected receipt: %s", out)
	}
	if len(docs.applied) != 1 {
		t.Fatalf("expected 1 applied edit, got %+v", docs.applied)
	}
	if docs.docs["r1/main.go"].Text != v1Content {
		t.Fatalf("open document text = %q, want %q", docs.docs["r1/main.go"].Text, v1Content)
	}
	_ = hex.EncodeToString
	_ = tools.ErrEditorReadRequired
}
