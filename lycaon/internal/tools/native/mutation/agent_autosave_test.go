package mutation

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// heldOverOutsideChange holds one path open whose file changed outside its
// document: saving there refuses, and nothing else is open.
type heldOverOutsideChange struct {
	tools.EditorDocuments
	held string
}

func (d heldOverOutsideChange) FreezeAgentReads(string, string) map[string]documentcore.ReadBasis {
	return nil
}

func (d heldOverOutsideChange) OpenDocument(context.Context, string, sourcebranch.ID, string, string) (tools.EditorDocumentText, bool, error) {
	return tools.EditorDocumentText{}, false, nil
}

func (d heldOverOutsideChange) VisitUnsavedDocuments(context.Context, string, sourcebranch.ID, string, string, bool, func(tools.EditorDocumentState) error) error {
	return nil
}

func (d heldOverOutsideChange) DirtyDocuments(context.Context, string, sourcebranch.ID) ([]tools.EditorDocumentPath, error) {
	return nil, nil
}

func (d heldOverOutsideChange) SaveForAgentOperation(_ context.Context, _ string, _ sourcebranch.ID, rootID, path string, descendants bool, _ tools.EditorAgentCall) error {
	if path == d.held || descendants && (path == "" || strings.HasPrefix(d.held, path+"/")) {
		return &tools.EditorFileChanged{RootID: rootID, Path: d.held, BaseSHA256: strings.Repeat("b", 64), DiskAbsent: true}
	}
	return nil
}

func (h *harness) tree() map[string]string {
	h.t.Helper()
	out := map[string]string{}
	testutil.FailErr(h.t, "walk", filepath.WalkDir(h.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == h.root {
			return err
		}
		rel, _ := filepath.Rel(h.root, path)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		body, readErr := os.ReadFile(path)
		out[rel] = string(body)
		return readErr
	}))
	return out
}

// Every utility that would change, move, copy from or remove a path the person
// holds over a file changed outside its document refuses with a revision
// conflict before anything on disk changes, whether the path is the operand
// itself, lies beneath a directory operand, or is a copy's destination entry.
func TestMutationRefusesAHeldPathWhoseFileChangedOutside(t *testing.T) {
	files := map[string]string{"held.txt": "saved\n", "other.txt": "other\n", "dir/held.txt": "saved\n", "dir/kept.txt": "kept\n", "empty/": ""}
	cases := []struct {
		name  string
		files map[string]string
		held  string
		words []string
	}{
		{"rm of the path", files, "held.txt", []string{"rm", "held.txt"}},
		{"rm -r beneath a directory", files, "dir/held.txt", []string{"rm", "-r", "dir"}},
		{"rmdir beneath a directory", files, "empty/held.txt", []string{"rmdir", "empty"}},
		{"mv of the path", files, "held.txt", []string{"mv", "held.txt", "moved.txt"}},
		{"mv beneath a directory", files, "dir/held.txt", []string{"mv", "dir", "moved"}},
		{"cp from the path", files, "held.txt", []string{"cp", "held.txt", "copy.txt"}},
		{"cp into a directory entry", files, "dir/other.txt", []string{"cp", "other.txt", "dir"}},
		{"patch of the path", threeFileTree(), "a.txt", []string{"patch", "-p1", "-i", "fix.diff"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.files)
			h.tc.Source.EditorDocuments = heldOverOutsideChange{held: tc.held}
			before := h.tree()
			r := h.run("", tc.words...)
			code := r.result.StopCode
			if reject := toolrejection.AsToolReject(r.err); reject != nil {
				code = reject.Code
			}
			if code != "COMMAND_REVISION_CONFLICT" {
				t.Fatalf("%v = %+v err=%v stderr=%q", tc.words, r.result, r.err, r.stderr)
			}
			if after := h.tree(); !maps.Equal(before, after) {
				t.Fatalf("%v changed the tree:\nbefore %v\nafter  %v", tc.words, before, after)
			}
		})
	}
}
