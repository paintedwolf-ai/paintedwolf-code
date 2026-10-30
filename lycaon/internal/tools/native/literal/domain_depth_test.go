package literal

import (
	"encoding/json"
	"strings"
	"testing"

	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRepoMapDepthUsesTheExistingMetadataOwner(t *testing.T) {
	h := domainHarness(t)
	for _, tc := range []struct {
		depth, resolution string
		store             bool
	}{
		{"0", "root_metadata", false},
		{"1", "depth_limited", false},
		{"2", "depth_limited", true},
	} {
		t.Run(tc.depth, func(t *testing.T) {
			run := h.exec("", "repo-map", "--depth", tc.depth)
			testutil.FailErr(t, "bounded repo-map", run.err)
			var response reposurvey.RepoResponse
			testutil.FailErr(t, "decode bounded repo-map", json.Unmarshal([]byte(run.stdout), &response))
			if run.result.Status != 0 || response.Resolution != tc.resolution {
				t.Fatalf("result %+v, response %+v", run.result, response)
			}
			found := false
			for _, entry := range response.Snapshot {
				found = found || strings.Contains(entry.Body, "store.go")
			}
			if found != tc.store {
				t.Fatalf("depth %s: store.go metadata visible = %v, want %v", tc.depth, found, tc.store)
			}
		})
	}
	h.exec("", "repo-map", "--depth", "-1").refused(t, "negative depth", "COMMAND_OPERAND_INVALID")
}
