package promptloop

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/zstdcodec"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGitDiffRetainsOriginalHunksBeforeClipping(t *testing.T) {
	dir := t.TempDir()
	diff := strings.Repeat("+observed before mutation\n", 1000)
	raw, err := git.MarshalDiffToolResponse(git.DiffToolResponse{MaxBytes: 100, Files: []git.DiffToolEntry{{Path: "a.go", Diff: diff}, {Path: "b.go", Diff: "+later\n"}}, FilesTotal: 2}, nil)
	testutil.FailErr(t, "marshal diff", err)
	out, reject := projectGitDiff(dir, tooloutput.Screened(raw), 0)
	if reject != nil {
		t.Fatalf("projection rejected: %+v", reject)
	}
	var got git.DiffToolResponse
	testutil.FailErr(t, "decode projection", json.Unmarshal([]byte(out), &got))
	if len(got.Files) != 1 || got.NextOffset == nil || *got.NextOffset != 1 || !got.Files[0].DiffTruncated || got.Files[0].DiffLines != 1000 {
		t.Fatalf("bad continuation: %+v", got)
	}
	for _, path := range []string{got.WireSpillPath, got.Files[0].DiffSpillPath} {
		stored, err := os.ReadFile(tooloutput.DiskPath(dir, path))
		testutil.FailErr(t, "read retained observation", err)
		body, err := zstdcodec.Decompress(stored)
		testutil.FailErr(t, "decode retained observation", err)
		if path == got.Files[0].DiffSpillPath && string(body) != diff {
			t.Fatal("raw hunks changed")
		}
		if path == got.WireSpillPath && !strings.Contains(string(body), "b.go") {
			t.Fatal("full page lost later file")
		}
	}
}

func TestGitDiffLargeHunksRetainExactObservation(t *testing.T) {
	dir := t.TempDir()
	diff := strings.Repeat("+captured original line\n", readcaps.MaxFileBytes/20)
	file := git.DiffToolEntry{Path: "large.txt", Diff: diff}
	if reject := retainDiffHunks(dir, &file, 0); reject != nil {
		t.Fatalf("retain large hunks: %+v", reject)
	}
	stored, err := os.ReadFile(tooloutput.DiskPath(dir, file.DiffSpillPath))
	testutil.FailErr(t, "read retained hunks", err)
	body, err := zstdcodec.Decompress(stored)
	testutil.FailErr(t, "decode retained hunks", err)
	if string(body) != diff {
		t.Fatal("lost original hunks above the project-file read limit")
	}
}

func TestGitDiffDoesNotDiscardWithoutRecovery(t *testing.T) {
	raw, err := git.MarshalDiffToolResponse(git.DiffToolResponse{MaxBytes: 10, Files: []git.DiffToolEntry{{Path: "a", Diff: strings.Repeat("x", 100)}}}, nil)
	testutil.FailErr(t, "marshal diff", err)
	if _, reject := projectGitDiff("", tooloutput.Screened(raw), 0); reject == nil {
		t.Fatal("clipped without recovery storage")
	}
	if _, reject := projectGitDiff(t.TempDir(), tooloutput.Screened(raw), 20); reject == nil {
		t.Fatal("ignored spill cap")
	}
}

func TestGitDiffSpillsOnlyScreenedHunks(t *testing.T) {
	dir := t.TempDir()
	loop := &PromptLoop{Deps: PromptLoopDeps{DataDir: dir, RedactMessageForStorage: testStorageRedactor}}
	diff := strings.Repeat("+unchanged\n", 100) + "+" + storageBoundarySecret + "\n"
	raw, err := git.MarshalDiffToolResponse(git.DiffToolResponse{MaxBytes: 100, Files: []git.DiffToolEntry{{Path: "secret.env", Diff: diff}}, FilesTotal: 1}, nil)
	testutil.FailErr(t, "marshal sensitive diff", err)
	projection := loop.projectToolResultForStorage(t.Context(), raw, nil)
	sess := &api.Session{ID: "screened-git", ProjectID: testdbseed.DefaultProjectID}
	got := loop.truncateToolResultForSession(t.Context(), "git_diff", projection, raw, 64000, 0, sess)
	if got.reject != nil {
		t.Fatalf("screened diff rejected: %+v", got.reject)
	}
	if strings.Contains(got.content, storageBoundarySecret) {
		t.Fatal("raw secret in model view")
	}
	for _, rel := range tooloutput.SpillPaths(got.content) {
		stored, err := os.ReadFile(tooloutput.DiskPath(project.HostDataDir(dir, sess.ProjectID), rel))
		testutil.FailErr(t, "read screened spill", err)
		body, err := zstdcodec.Decompress(stored)
		testutil.FailErr(t, "decode screened spill", err)
		assertNoSecretFragment(t, "retained Git observation", string(body))
	}
}
