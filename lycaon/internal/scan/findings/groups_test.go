package findings

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestFindingGroupsCountBeforeSamplingAndKeepPackagesSeparate(t *testing.T) {
	var findings []api.SecurityFinding
	for i := 0; i < 1000; i++ {
		findings = append(findings, BuildSecurityFinding(FindingBuildOpts{DriverID: "scanner", RuleID: "rule", Level: api.FindingLevelHigh, Locations: []api.SecurityFindingLocation{{URI: fmt.Sprintf("src/%04d", i), StartLine: 1}}}))
	}
	for _, name := range []string{"a", "b"} {
		findings = append(findings, BuildSecurityFinding(FindingBuildOpts{DriverID: "sca", RuleID: "CVE-2026-12345", Kind: api.FindingKindSCA, Level: api.FindingLevelUnknown, Advisory: &api.AdvisoryRef{OSVID: "CVE-2026-12345", Package: &api.AdvisoryPackageRef{Name: name, Version: "1", Ecosystem: "go"}}}))
	}
	groups := GroupFindings(findings, 3)
	if len(groups) != 3 || groups[0].Count != 1000 || groups[0].LocationCount != 1000 || len(groups[0].Locations) != 3 {
		t.Fatalf("groups = %#v", groups)
	}
	slices.Reverse(findings)
	if got := GroupFindings(findings, 3); !reflect.DeepEqual(groups, got) {
		t.Fatal("grouping depends on input order")
	}
}
