package literal

import (
	"encoding/json"
	"testing"

	reposurvey "github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestRepoMapAliasUsesCanonicalOwnerAndActualArguments(t *testing.T) {
	h := domainHarness(t)
	paths := mustPaths(t, h)
	request := requestFor(h, "repo-map", "--path", "pkg")
	request.Argv[0].Value = "survey-repo"
	streams := streamsFor("")
	prepared, err := h.owners.Prepare(paths, request)
	testutil.FailErr(t, "prepare repo-map alias", err)
	result, err := prepared.InvokeOwner(t.Context(), h.tc, streams)
	testutil.FailErr(t, "invoke repo-map alias", err)
	if result.Status != 0 || result.Terminal != toolcontract.ProducerCompleted {
		t.Fatalf("alias result = %+v", result)
	}
	var response reposurvey.RepoResponse
	testutil.FailErr(t, "decode alias response", json.Unmarshal(streams.Output.(*mappingBuffer).Bytes(), &response))
	if response.Bundle != "layout_overview" || response.Digest == "" {
		t.Fatalf("alias response = %+v", response)
	}
	request.Argv[0].Value = "cat"
	if _, err := h.owners.Prepare(paths, request); err == nil {
		t.Fatal("canonical owner accepted an undeclared command spelling")
	}
}
