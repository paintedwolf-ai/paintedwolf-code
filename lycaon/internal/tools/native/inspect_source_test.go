package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
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

func writeGitInspectionFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	target := filepath.Join(dir, ".git", filepath.FromSlash(name))
	testutil.FailErr(t, "make metadata fixture directory", os.MkdirAll(filepath.Dir(target), 0o700))
	testutil.FailErr(t, "write metadata fixture", os.WriteFile(target, []byte(content+"\n"), 0o600))
}
