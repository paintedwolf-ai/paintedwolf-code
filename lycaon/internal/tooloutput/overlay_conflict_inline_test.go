package tooloutput_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildConflictDigestOmitsHunkBodies(t *testing.T) {
	digest := tooloutput.BuildConflictDigest([]api.WorkerMergeConflict{{
		Path: "a.go",
		Hunks: []api.WorkerMergeHunk{{
			StartLine: 1,
			EndLine:   3,
			Primary:   "primary block",
			Branch:    "branch block",
		}},
	}})
	if len(digest) != 1 {
		t.Fatalf("digest = %+v", digest)
	}
	encoded, err := json.Marshal(digest[0])
	testutil.FailErr(t, "json.Marshal failed", err)
	if strings.Contains(string(encoded), `"hunks"`) {
		t.Fatalf("inline digest must not serialize hunks: %s", encoded)
	}
	if len(digest[0].Summary) == 0 {
		t.Fatal("expected line-range summary")
	}
}
