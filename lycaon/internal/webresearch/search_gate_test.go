package webresearch_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webresearch"
)

func TestFilterSearchToolRemovesWebResearchTools(t *testing.T) {
	got := webresearch.FilterSearchTool([]string{"read", "web_search", "fetch_url"})
	if len(got) != 1 || got[0] != "read" {
		t.Fatalf("got = %v", got)
	}
}

func TestSearchToolRuntimeDeny(t *testing.T) {
	cfg := webresearch.NewConfigStoreAt(t.TempDir() + "/cfg.yaml")
	deny := webresearch.SearchToolRuntimeDeny(cfg)
	if deny("web_search") {
		t.Fatal("search should be enabled by default")
	}
	disabled := false
	testutil.FailErr(t, "ApplyPrefs", cfg.ApplyPrefs(nil, nil, &disabled, nil))
	if !deny("web_search") {
		t.Fatal("expected web_search denied when search disabled")
	}
	if !deny("fetch_url") {
		t.Fatal("expected fetch_url denied when search disabled")
	}
}
