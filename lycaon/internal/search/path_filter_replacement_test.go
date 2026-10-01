package search

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryFilterSearchReplacementParity(t *testing.T) {
	dir := t.TempDir()
	paths := []string{"src/pkg/a.txt", "src/pkg/deep/b.txt", "src/pkg-other/c.txt", "src/under_score/d.txt"}
	for _, path := range paths {
		abs := filepath.Join(dir, path)
		testutil.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write fixture", os.WriteFile(abs, []byte("old\n"), 0o644))
	}
	p, err := project.CreateWithRoot(context.Background(), project.NewMemoryRegistry(), dir)
	testutil.FailErr(t, "create project", err)
	root := CodeRoot{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: dir}
	store := testReplaceStore{project: p}
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"src/pkg/", paths[:2]},
		{"/SRC/pkg/", paths[:2]},
		{"src/pk/", nil},
		{"src/*/", paths},
		{"src/under_score/", paths[3:]},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			query, parseErr := ParseQuery("old kind:code path:" + tc.pattern)
			testutil.FailErr(t, "parse directory query", parseErr)
			hits := runCodeLeg(t, &CodePlanLeg{
				Query: query, PathRoots: []CodeRoot{root}, Lines: true, Cap: SearchExecutorProbeHits,
			})
			preview, previewErr := settledReplacePreview(context.Background(), ReplacePreviewRequest{
				Query: query, Replacement: "new", Roots: []CodeRoot{root},
			})
			testutil.FailErr(t, "preview directory replacement", previewErr)
			var previewPaths []string
			for _, file := range preview.Files {
				previewPaths = append(previewPaths, file.Path)
			}
			var files []ReplaceApplyFile
			for _, path := range paths {
				read, readErr := store.ReadReplaceContent(root.RootID, path)
				testutil.FailErr(t, "read candidate", readErr)
				files = append(files, ReplaceApplyFile{RootID: root.RootID, Path: path, SHA256: read.SHA256, HunkIndexes: []int{0}})
			}
			plan, planErr := PlanReplace(ReplacePlanRequest{Query: query, Replacement: "new", Store: store, Files: files})
			testutil.FailErr(t, "plan directory replacement", planErr)
			var writePaths []string
			for _, write := range plan.Writes {
				writePaths = append(writePaths, write.Path)
			}
			for stage, got := range map[string][]string{
				"search": hitPathsOfKind(hits, HitKindCode), "preview": previewPaths, "apply": writePaths,
			} {
				slices.Sort(got)
				want := slices.Sorted(slices.Values(tc.want))
				if !slices.Equal(got, want) {
					t.Errorf("%s paths = %v, want %v", stage, got, want)
				}
			}
		})
	}
}
