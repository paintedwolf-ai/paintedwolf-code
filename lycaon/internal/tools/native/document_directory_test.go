package native

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestSharedDocumentDirectoryRefusalRemainsTyped(t *testing.T) {
	docs := &fakeEditorDocuments{openErr: projectsource.ErrSourceIsDirectory}
	resolved := projectpaths.Resolved{Root: projectroot.RootRef{ID: "r1"}, ScopeRel: "directory", DisplayPath: "directory"}
	_, open, err := sourceview.OpenDocument(t.Context(), editorCtx(t.TempDir(), docs), resolved)
	var reject *toolrejection.ToolReject
	if open || !errors.As(err, &reject) || reject.Code != "READ_IS_DIRECTORY" || reject.Data["path"] != "directory" {
		t.Fatalf("directory lookup open%v err%v", open, err)
	}
}
