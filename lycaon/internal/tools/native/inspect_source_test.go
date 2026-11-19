package native

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestInspectSourceReadsGitHeadFromBoundedMetadata(t *testing.T) {
	oid := strings.Repeat("a", 40)
	const ref = "refs/heads/review"
	for _, tc := range []struct {
		name, head, file, payload string
		want                      gitHead
	}{
		{name: "detached", head: oid, want: gitHead{Repository: true, Commit: oid}},
		{name: "loose", head: "ref: " + ref, file: ref, payload: oid, want: gitHead{Repository: true, Reference: ref, Commit: oid}},
		{name: "packed", head: "ref: " + ref, file: "packed-refs", payload: oid + " " + ref, want: gitHead{Repository: true, Reference: ref, Commit: oid}},
		{name: "unborn", head: "ref: " + ref, want: gitHead{Repository: true, Reference: ref, Unborn: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGitInspectionFixture(t, dir, "HEAD", tc.head)
			if tc.file != "" {
				writeGitInspectionFixture(t, dir, tc.file, tc.payload)
			}
			ctx := provenanceCtx(dir, nil)
			ctx.Source.AskMode, ctx.Source.ReadOnly = true, true
			tool := &InspectSourceTool{Boundary: nativefixture.Boundary(t)}
			output, err := tool.Run(t.Context(), map[string]any{"operation": "git_head"}, ctx)
			testutil.FailErr(t, "inspect Git HEAD", err)
			var got gitHead
			testutil.FailErr(t, "decode Git observation", json.Unmarshal([]byte(output), &got))
			if got != tc.want {
				t.Fatalf("Git HEAD = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestInspectSourceRejectsUnsafeGitReference(t *testing.T) {
	dir := t.TempDir()
	writeGitInspectionFixture(t, dir, "HEAD", "ref: refs/../../elsewhere")
	tool := &InspectSourceTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(t.Context(), map[string]any{"operation": "git_head"}, provenanceCtx(dir, nil))
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
		t.Fatalf("malformed symbolic head was not rejected: %v", err)
	}
}

func TestInspectSourceReadsLinkedWorktreeHead(t *testing.T) {
	dir := t.TempDir()
	checkout := filepath.Join(dir, "checkout")
	testutil.FailErr(t, "make worktree", os.Mkdir(checkout, 0o700))
	testutil.FailErr(t, "write worktree pointer", os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: ../.git/worktrees/checkout\n"), 0o600))
	writeGitInspectionFixture(t, dir, "worktrees/checkout/HEAD", "ref: refs/heads/review")
	writeGitInspectionFixture(t, dir, "worktrees/checkout/commondir", "../..")
	oid := strings.Repeat("b", 40)
	writeGitInspectionFixture(t, dir, "refs/heads/review", oid)

	tool := &InspectSourceTool{Boundary: nativefixture.Boundary(t)}
	got, err := tool.inspectGitHead(t.Context(), provenanceCtx(dir, nil), "checkout")
	testutil.FailErr(t, "inspect linked worktree HEAD", err)
	if want := (gitHead{Repository: true, Reference: "refs/heads/review", Commit: oid}); got != want {
		t.Fatalf("Git HEAD = %+v, want %+v", got, want)
	}
}

func TestInspectSourceRejectsExternalGitMetadata(t *testing.T) {
	for _, name := range []string{".git", "commondir"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			outside := t.TempDir()
			t.Setenv("LYCAON_SANDBOX_DENY_READ", outside)
			external := filepath.Join(outside, "metadata")
			testutil.FailErr(t, "write external metadata", os.WriteFile(external, nil, 0o600))
			rel := ".git"
			if name == "commondir" {
				writeGitInspectionFixture(t, dir, "HEAD", "ref: refs/heads/review")
				rel = filepath.Join(rel, name)
			}
			testutil.FailErr(t, "link external metadata", os.Symlink(external, filepath.Join(dir, rel)))

			tool := &InspectSourceTool{Boundary: nativefixture.Boundary(t)}
			_, err := tool.inspectGitHead(t.Context(), provenanceCtx(dir, nil), ".")
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != isolation.CodeControlPlaneDenied || reject.Data["path"] != fspath.CanonicalPath(external) {
				t.Fatalf("external metadata %s was not rejected at its read boundary: %v", name, err)
			}
		})
	}
}

func TestInspectSourceIgnoresMissingOptionalMetadataOutsideReadGlobs(t *testing.T) {
	dir := t.TempDir()
	writeGitInspectionFixture(t, filepath.Join(dir, "checkout"), "HEAD", "ref: refs/heads/review")
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID:        toolprofiles.DefaultToolProfileID,
		ReadGlobs: []string{"checkout", "checkout/.git", "checkout/.git/HEAD"},
	}})
	tool := &InspectSourceTool{Boundary: boundary}
	got, err := tool.inspectGitHead(t.Context(), provenanceCtx(dir, nil), "checkout")
	testutil.FailErr(t, "inspect unborn HEAD", err)
	if !got.Unborn {
		t.Fatalf("Git HEAD = %+v, want unborn branch", got)
	}
}

func TestInspectSourceRejectsOversizedCommonDir(t *testing.T) {
	dir := t.TempDir()
	writeGitInspectionFixture(t, dir, "HEAD", "ref: refs/heads/review")
	writeGitInspectionFixture(t, dir, "commondir", strings.Repeat("x", 4097))
	tool := &InspectSourceTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.inspectGitHead(t.Context(), provenanceCtx(dir, nil), ".")
	var tooLarge *projectpaths.FileTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 4096 {
		t.Fatalf("oversized commondir was not refused at the read limit: %v", err)
	}
}

func writeGitInspectionFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	target := filepath.Join(dir, ".git", filepath.FromSlash(name))
	testutil.FailErr(t, "make metadata fixture directory", os.MkdirAll(filepath.Dir(target), 0o700))
	testutil.FailErr(t, "write metadata fixture", os.WriteFile(target, []byte(content+"\n"), 0o600))
}
