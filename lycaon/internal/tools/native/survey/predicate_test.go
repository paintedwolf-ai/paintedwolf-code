package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestPredicateFilesUsesAdmittedReadAuthority(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "fixture directory", os.Mkdir(filepath.Join(root, "allowed"), 0700))
	testutil.FailErr(t, "fixture file", os.WriteFile(filepath.Join(root, "allowed", "file"), []byte("unchanged"), 0600))
	tctx := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Label: "fixture", Path: root, IsPrimary: true}}, ActiveRootID: "root"}, Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID}}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"allowed/**"}}})
	reads := projectpaths.NewReadSession(boundary, tctx)
	defer reads.Close()
	invocation := toolcontract.OperationInvocation{Kind: toolcontract.StructuredToolInvocation, ModelToolCallID: "fixture-call", ActorID: "fixture-actor", SessionID: "fixture-session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "fixture-project"}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	port := PredicateFiles{Reads: reads, Invocation: invocation}
	for _, op := range []string{"-e", "-f", "-r", "-w"} {
		value, err := port.TestPath(t.Context(), invocation, op, "allowed/file")
		testutil.FailErr(t, "authority-backed predicate", err)
		if !value {
			t.Fatalf("expected %s file", op)
		}
	}
	missing, err := port.TestPath(t.Context(), invocation, "-e", "allowed/missing")
	testutil.FailErr(t, "missing allowed file", err)
	if missing {
		t.Fatal("missing file exists")
	}
	if _, err := port.TestPath(t.Context(), invocation, "-e", "outside-read-glob"); err == nil {
		t.Fatal("denied metadata was booleanized")
	}
	different := invocation
	different.ActorID = "another-actor"
	if _, err := port.TestPath(t.Context(), different, "-f", "allowed/file"); err == nil {
		t.Fatal("invocation rebound")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := port.TestPath(ctx, invocation, "-e", "allowed/file"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	bytes, err := os.ReadFile(filepath.Join(root, "allowed", "file"))
	testutil.FailErr(t, "fixture bytes after writable query", err)
	if string(bytes) != "unchanged" {
		t.Fatalf("writable query changed bytes %q", bytes)
	}
}
