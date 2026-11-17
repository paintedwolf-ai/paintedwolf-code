package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
	"golang.org/x/sys/unix"
)

func TestWriteRejectsDirectoriesAndEscapes(t *testing.T) {
	adapter, _, project, rootID := newAdapterFixture(t)
	root := project.Roots[0].Path
	testutil.FailErr(t, "mkdir", os.Mkdir(filepath.Join(root, "src"), 0o755))
	testutil.FailErr(t, "fifo", unix.Mkfifo(filepath.Join(root, "pipe"), 0o600))
	outside := t.TempDir()
	testutil.FailErr(t, "outside", os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644))
	testutil.FailErr(t, "link", os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape.txt")))
	names := map[string]bool{"write": true}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "coordinator", Tools: names}})
	for _, withDocs := range []bool{false, true} {
		tctx := tools.ToolContext{
			Identity: tools.InvocationIdentity{ProjectID: project.ID, SessionID: "s", Agent: "coordinator"},
			Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: rootID, Label: "root", Path: root, IsPrimary: true}}, ActiveRootID: rootID, SourceWorkspaceKind: api.SourceWorkspaceKindProject, RepoFileCount: 1, RepoFileCountKnown: true},
		}
		if withDocs {
			tctx.Source.EditorDocuments = adapter
		}
		write := func(p string) error {
			_, err := (&native.WriteTool{Boundary: boundary}).Run(context.Background(), map[string]any{"path": p, "content": "x\n"}, tctx)
			return err
		}
		for _, p := range []string{".", "src"} {
			var reject *toolrejection.ToolReject
			if err := write(p); !errors.As(err, &reject) || reject.Code != "READ_IS_DIRECTORY" {
				t.Fatalf("docs=%v write %q: want READ_IS_DIRECTORY, got err=%v", withDocs, p, err)
			}
		}
		var reject *toolrejection.ToolReject
		if err := write("escape.txt"); !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
			t.Fatalf("docs=%v write escape.txt: want SURVEY_PATH_ESCAPE, got err=%v", withDocs, err)
		}
	}
}
