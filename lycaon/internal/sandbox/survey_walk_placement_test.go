package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWalkersNeverEnterAPlacementFolder(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, enginepaths.WorkerPlacementDirName(501, "/state"))
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(folder, "worker-branches"), 0o700))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "kept.txt"), nil, 0o600))
	var seen []string
	err := SurveyWalk(context.Background(), root, SurveyOptions{IncludeHidden: true, IncludeEngineOverlay: true, IncludeVCSMetadata: true},
		func(entry SurveyEntry) (SurveyAction, error) {
			seen = append(seen, entry.Rel)
			return SurveyContinue, nil
		})
	testutil.FailErr(t, "walk", err)
	if len(seen) != 1 || seen[0] != "kept.txt" {
		t.Fatalf("seen = %v", seen)
	}
	if !ShouldSkipDirBaseName(filepath.Base(folder)) || !ShouldSkipDir(filepath.Base(folder), filepath.Base(folder)) {
		t.Fatal("index walkers would enter the folder")
	}
}
