package survey

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestEncodeRepoResponseFitsSnapshotAsValidJSON(t *testing.T) {
	resp := repoResponse{
		Bundle:    "large",
		Path:      ".",
		View:      repoViewDigest,
		Digest:    "complete digest",
		Total:     1_000,
		ProbesRun: 60,
		Snapshot:  make([]snapshotEntry, 1_000),
	}
	for i := range resp.Snapshot {
		resp.Snapshot[i] = snapshotEntry{
			Handle: fmt.Sprintf("snap#%04d", i),
			Kind:   "grep",
			Path:   fmt.Sprintf("internal/package_%04d/file.go", i),
			Body:   strings.Repeat("x", 200),
		}
	}

	originalCount := len(resp.Snapshot)
	out, err := capRepoResponse(&resp)
	testutil.FailErr(t, "encode repo response", err)
	if len(out) > surveyRepoMaxResponseBytes {
		t.Fatalf("response bytes = %d want <= %d", len(out), surveyRepoMaxResponseBytes)
	}
	var decoded repoResponse
	testutil.FailErr(t, "decode repo response", json.Unmarshal([]byte(out), &decoded))
	if !decoded.Truncated {
		t.Fatal("expected truncated snapshot")
	}
	if decoded.Total != resp.Total || decoded.Digest != resp.Digest {
		t.Fatalf("coverage changed: total=%d digest=%q", decoded.Total, decoded.Digest)
	}
	if len(decoded.Snapshot) == 0 || len(decoded.Snapshot) >= originalCount {
		t.Fatalf("snapshot size = %d want a non-empty fitted prefix", len(decoded.Snapshot))
	}
	capture := tools.ToolContext{ProjectID: "p", ActiveRootID: "r", Roots: []projectroot.RootRef{{ID: "r", Path: t.TempDir()}}, Out: &tools.ToolInvocationOut{}}
	recordRepoSources(capture, resp)
	if capture.Out.SourceContext == nil || len(capture.Out.SourceContext.Locations) != len(decoded.Snapshot) {
		t.Fatalf("fitted survey sources = %+v", capture.Out.SourceContext)
	}

}
