package researchwarm

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestIndexWarmingSummaryLine(t *testing.T) {
	got := indexWarmingSummary(api.IndexWarmingMeta{
		Topic: "steam machine", Hosts: []string{"a", "b"}, Pages: 1, DurationMs: 1200,
	})
	if got != "Warmed web index — 2 hosts, 1 page · steam machine" {
		t.Fatalf("summary = %q", got)
	}
	skip := indexWarmingSummary(api.IndexWarmingMeta{Topic: "x", SkipReason: "hourly seed cap"})
	if !strings.Contains(skip, "hourly seed cap") {
		t.Fatalf("summary = %q", skip)
	}
	partial := indexWarmingSummary(api.IndexWarmingMeta{
		Topic: "widget", Hosts: []string{"a.example"}, Pages: 2, SkipReason: "hourly seed cap",
	})
	if strings.Contains(partial, "hourly seed cap") {
		t.Fatalf("summary = %q want no skip when crawl succeeded", partial)
	}
}
