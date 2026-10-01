package findings_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/advisory"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

func scaRow(driver, uri string, level api.FindingLevel, source string, ids ...string) api.SecurityFinding {
	adv := advisory.BuildAdvisoryRef(ids[0], ids[1:]...)
	adv.Kind = api.AdvisoryKindVulnerability
	adv.SeveritySource = source
	adv.Package = &api.AdvisoryPackageRef{Name: "example.org/mod", Version: "1.0.0", Ecosystem: "Go"}
	return scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: driver, RuleID: advisory.RuleID(adv), Level: level, Message: ids[0] + " summary",
		Kind: api.FindingKindSCA, Locations: []api.SecurityFindingLocation{{URI: uri}}, Advisory: adv,
	})
}

func TestMergeByAdvisoryFoldsDatabaseRecordIntoItsScoredTwin(t *testing.T) {
	// The matching advisory supplies the missing score.
	goRecord := scaRow("lycaon-sca", "go.mod", api.FindingLevelUnknown, "", "GO-2020-0001", "CVE-2020-36567", "GHSA-6vm3-jj99-7229")
	ghsaRecord := scaRow("lycaon-sca", "go.mod", api.FindingLevelHigh, "osv.cvss", "GHSA-6vm3-jj99-7229", "CVE-2020-36567", "GO-2020-0001")
	merged := scanfindings.MergeByAdvisory([]api.SecurityFinding{goRecord, ghsaRecord})
	if len(merged) != 1 {
		t.Fatalf("merged = %d rows, want 1", len(merged))
	}
	row := merged[0]
	adv := row.Properties.Lycaon.Advisory
	if row.Level != api.FindingLevelHigh || adv.SeveritySource != "osv.cvss" {
		t.Fatalf("level = %s / %s, want high from osv.cvss", row.Level, adv.SeveritySource)
	}
	if row.RuleID != "osv:CVE-2020-36567" || adv.OSVID != "CVE-2020-36567" {
		t.Fatalf("identity = %s / %s, want canonical CVE", row.RuleID, adv.OSVID)
	}
	if !reflect.DeepEqual(adv.Aliases, []string{"GO-2020-0001"}) || !reflect.DeepEqual(adv.GHSAIDs, []string{"GHSA-6vm3-jj99-7229"}) {
		t.Fatalf("alias families = %#v", adv)
	}
	if !reflect.DeepEqual(row.Properties.Lycaon.Sources, []string{"lycaon-sca"}) {
		t.Fatalf("sources = %#v", row.Properties.Lycaon.Sources)
	}
}

func TestMergeByAdvisoryJoinsThroughAnySharedIDAndRekeysTheUnion(t *testing.T) {
	// Shared advisory aliases connect all three records.
	goRecord := scaRow("lycaon-sca", "go.mod", api.FindingLevelUnknown, "", "GO-2020-0001", "GHSA-6vm3-jj99-7229")
	ghsaRecord := scaRow("lycaon-sca", "go.mod", api.FindingLevelMedium, "osv.cvss", "GHSA-6vm3-jj99-7229", "CVE-2020-36567")
	engineRow := scaRow("trivy", "vendor/modules.txt", api.FindingLevelHigh, "vendor", "CVE-2020-36567")
	if goRecord.RuleID != "osv:GHSA-6vm3-jj99-7229" {
		t.Fatalf("fixture must start under a non-canonical id, got %s", goRecord.RuleID)
	}
	var want api.SecurityFinding
	for i, order := range [][]api.SecurityFinding{
		{goRecord, ghsaRecord, engineRow},
		{engineRow, goRecord, ghsaRecord},
		{ghsaRecord, engineRow, goRecord},
		{goRecord, engineRow, ghsaRecord},
	} {
		merged := scanfindings.MergeByAdvisory(order)
		if len(merged) != 1 {
			t.Fatalf("order %d: merged = %d rows, want 1", i, len(merged))
		}
		if i == 0 {
			want = merged[0]
			continue
		}
		if !reflect.DeepEqual(merged[0], want) {
			t.Fatalf("order %d differs:\n%#v\n%#v", i, merged[0], want)
		}
	}
	adv := want.Properties.Lycaon.Advisory
	if want.RuleID != "osv:CVE-2020-36567" || adv.OSVID != "CVE-2020-36567" || want.Level != api.FindingLevelHigh || adv.SeveritySource != "vendor" {
		t.Fatalf("union identity = %s %s %s/%s", want.RuleID, adv.OSVID, want.Level, adv.SeveritySource)
	}
	if got := advisory.IDs(adv); !reflect.DeepEqual(got, []string{"CVE-2020-36567", "GHSA-6vm3-jj99-7229", "GO-2020-0001"}) {
		t.Fatalf("ids = %#v", got)
	}
	if got := slices.Clone(want.Properties.Lycaon.Sources); !reflect.DeepEqual(got, []string{"lycaon-sca", "trivy"}) {
		t.Fatalf("sources = %#v", got)
	}
	if scanfindings.PrimaryURI(want) != "go.mod" {
		t.Fatalf("primary uri = %q, want the lockfile", scanfindings.PrimaryURI(want))
	}
	if want.Fingerprints.Primary != scanfindings.PrimaryFingerprint("lycaon-sca", api.FindingKindSCA, "osv:CVE-2020-36567", "go.mod", 0, "CVE-2020-36567") {
		t.Fatalf("fingerprint not recomputed for the canonical identity")
	}
}

func TestMergeByAdvisoryKeepsSamePackageDifferentVulnerabilitiesApart(t *testing.T) {
	first := scaRow("lycaon-sca", "go.mod", api.FindingLevelHigh, "osv.cvss", "CVE-2024-0001")
	second := scaRow("lycaon-sca", "go.mod", api.FindingLevelLow, "osv.cvss", "CVE-2024-0002")
	if got := scanfindings.MergeByAdvisory([]api.SecurityFinding{first, second}); len(got) != 2 {
		t.Fatalf("merged = %d rows, want 2", len(got))
	}
}

func TestMergeByAdvisoryLeavesSoloRowsUntouched(t *testing.T) {
	solo := scaRow("lycaon-sca", "go.mod", api.FindingLevelUnknown, "", "GO-2020-0001")
	merged := scanfindings.MergeByAdvisory([]api.SecurityFinding{solo})
	if len(merged) != 1 || merged[0].RuleID != solo.RuleID || merged[0].Fingerprints.Primary != solo.Fingerprints.Primary || merged[0].Level != api.FindingLevelUnknown {
		t.Fatalf("solo row changed: %#v", merged[0])
	}
}
