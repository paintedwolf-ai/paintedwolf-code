package coverage

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestScopeConservesFullCountsAndBoundsPresentation(t *testing.T) {
	var warnings []api.ScanWarning
	for i := range 36 {
		for range 2 {
			warnings = append(warnings, api.ScanWarning{File: fmt.Sprintf("component-%02d/src/file.ext%d", i, i), Construct: fmt.Sprintf("construct-%02d", i)})
		}
	}
	scope := Summarize(warnings)
	if scope.Files != 36 || scope.Warnings != 72 || len(scope.PathsSample) != SampleLimit || !scope.SampleTruncated {
		t.Fatalf("scope = %+v", scope)
	}
	for _, row := range []struct {
		distribution Distribution
		total        int
	}{{scope.Directories, 36}, {scope.Extensions, 36}, {scope.Constructs, 72}} {
		count := row.distribution.Other
		for _, entry := range row.distribution.Entries {
			count += entry.Count
		}
		if count != row.total || len(row.distribution.Entries) > DistributionLimit {
			t.Fatalf("lost or unbounded counts: %+v", row)
		}
	}
	slices.Reverse(warnings)
	if !reflect.DeepEqual(scope, Summarize(warnings)) {
		t.Fatal("warning order changed scope")
	}
	if scope.PathsSample[0] != warnings[len(warnings)-1].File || scope.PathsSample[SampleLimit-1] != warnings[0].File {
		t.Fatal("sample did not span full path range")
	}
}

func TestScopeDoesNotPromoteNamesToClassification(t *testing.T) {
	warnings := []api.ScanWarning{{File: "examples/shipped/service.rs"}, {File: "tests/production.go"}, {File: "src/fixture.go"}, {}}
	scope := Summarize(warnings)
	if scope.Files != 3 || scope.Warnings != 4 || scope.SampleTruncated {
		t.Fatalf("scope = %+v", scope)
	}
	if len(scope.Constructs.Entries) != 1 || scope.Constructs.Entries[0].Count != 4 {
		t.Fatalf("unknown constructs lost: %+v", scope.Constructs)
	}
}
