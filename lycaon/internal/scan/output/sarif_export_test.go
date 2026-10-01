package output

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/advisory"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/owenrumney/go-sarif/v2/sarif"
)

func TestExportSARIFBuildsRuns(t *testing.T) {
	data, err := ExportSARIF([]api.SecurityFinding{
		scanfindings.FixtureFinding("opengrep:test-rule", api.FindingLevelHigh, "example finding", "src/a.go", 10),
	}, false)
	if err != nil {
		t.Fatalf("ExportSARIF: %v", err)
	}
	if !strings.Contains(string(data), `"version"`) {
		t.Fatalf("missing version: %s", data)
	}
	if !strings.Contains(string(data), "opengrep:test-rule") {
		t.Fatalf("missing rule id: %s", data)
	}
}

func TestExportSARIFPreservesCriticalExtension(t *testing.T) {
	finding := scanfindings.FixtureFinding("critical", api.FindingLevelCritical, "critical", "a.go", 1)
	data, err := ExportSARIF([]api.SecurityFinding{finding}, false)
	if err != nil {
		t.Fatalf("ExportSARIF: %v", err)
	}
	parsed, err := ParseOutput(OutputParserSARIF, data)
	if err != nil {
		t.Fatalf("ParseOutput: %v", err)
	}
	if parsed.Findings[0].Level != api.FindingLevelCritical {
		t.Fatalf("level = %q want critical", parsed.Findings[0].Level)
	}
}

func TestExportSARIFPreservesNormalizedFindingShape(t *testing.T) {
	adv := advisory.BuildAdvisoryRef("GO-2026-0001", "CVE-2026-1234", "GHSA-6vm3-jj99-7229")
	adv.Kind = api.AdvisoryKindVulnerability
	adv.Package = &api.AdvisoryPackageRef{Name: "example/module", Version: "1.2.3", Ecosystem: "gomod"}
	finding := scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
		DriverID: "custom-scanner", ToolName: "Custom Scanner", ToolVersion: "2.0.0",
		RuleID: "rule-without-namespace", Level: api.FindingLevelInfo, Message: "message",
		Kind: api.FindingKindSCA, Advisory: adv,
		Categories: []api.ScanCategory{api.ScanCategorySCA, api.ScanCategorySecurity},
		Locations: []api.SecurityFindingLocation{
			{URI: "go.mod", StartLine: 2, StartColumn: 3, EndLine: 4, EndColumn: 5},
			{URI: "go.sum", StartLine: 8},
		},
	})
	finding.Properties.Lycaon.HintCode = "SCAN_FIX"
	finding.Properties.Lycaon.Sources = []string{"first", "second"}

	data, err := ExportSARIF([]api.SecurityFinding{finding}, false)
	if err != nil {
		t.Fatalf("ExportSARIF: %v", err)
	}
	report, err := sarif.FromBytes(data)
	if err != nil {
		t.Fatalf("sarif.FromBytes: %v", err)
	}
	driver := report.Runs[0].Tool.Driver
	if driver.GUID != nil {
		t.Fatalf("non-UUID driver id was emitted as SARIF guid: %q", *driver.GUID)
	}
	result := report.Runs[0].Results[0]
	if result.Level == nil || *result.Level != "none" {
		t.Fatalf("level = %v want none", result.Level)
	}
	if got := result.Fingerprints["primary"]; got != finding.Fingerprints.Primary {
		t.Fatalf("fingerprint = %v", got)
	}
	if len(result.Locations) != 2 || result.Locations[0].PhysicalLocation.Region.EndColumn == nil ||
		*result.Locations[0].PhysicalLocation.Region.EndColumn != 5 {
		t.Fatalf("locations = %#v", result.Locations)
	}

	parsed, err := ParseOutput(OutputParserSARIF, data)
	if err != nil {
		t.Fatalf("ParseOutput: %v", err)
	}
	got := parsed.Findings[0]
	if got.RuleID != finding.RuleID || got.Tool.DriverID != finding.Tool.DriverID || got.Tool.Version != finding.Tool.Version || got.Level != finding.Level ||
		len(got.Locations) != 2 || got.Fingerprints.Primary != finding.Fingerprints.Primary {
		t.Fatalf("round trip finding = %#v", got)
	}
	if got.Properties == nil || got.Properties.Lycaon == nil || got.Properties.Lycaon.Advisory == nil ||
		got.Properties.Lycaon.Advisory.Package == nil || got.Properties.Lycaon.Advisory.Package.Ecosystem != "go" ||
		got.Properties.Lycaon.HintCode != "SCAN_FIX" || len(got.Properties.Lycaon.Sources) != 2 {
		t.Fatalf("round trip properties = %#v", got.Properties)
	}
	gotAdv := got.Properties.Lycaon.Advisory
	if gotAdv.OSVID != "CVE-2026-1234" || gotAdv.Kind != api.AdvisoryKindVulnerability ||
		!reflect.DeepEqual(gotAdv.GHSAIDs, []string{"GHSA-6vm3-jj99-7229"}) || !reflect.DeepEqual(gotAdv.Aliases, []string{"GO-2026-0001"}) {
		t.Fatalf("round trip advisory identity = %#v", gotAdv)
	}
}

func TestExportSARIFGroupsRunsByDriver(t *testing.T) {
	data, err := ExportSARIF([]api.SecurityFinding{
		scanfindings.FixtureFinding("r1", api.FindingLevelHigh, "a", "a.go", 1),
		scanfindings.BuildSecurityFinding(scanfindings.FindingBuildOpts{
			DriverID: "gitleaks", ToolName: "Gitleaks", RuleID: "gitleaks:pat",
			Level: api.FindingLevelHigh, Message: "secret", Kind: api.FindingKindSecret,
			Locations: []api.SecurityFindingLocation{{URI: "secrets.env", StartLine: 2}},
		}),
	}, false)
	if err != nil {
		t.Fatalf("ExportSARIF: %v", err)
	}
	report, err := sarif.FromBytes(data)
	if err != nil {
		t.Fatalf("sarif.FromBytes: %v", err)
	}
	if len(report.Runs) != 2 {
		t.Fatalf("runs = %d want 2", len(report.Runs))
	}
	for _, run := range report.Runs {
		if len(run.Tool.Driver.Rules) != 1 || run.Tool.Driver.Rules[0].DefaultConfiguration == nil {
			t.Fatalf("driver rules = %#v", run.Tool.Driver.Rules)
		}
	}
}

func TestExportSARIFTruncationProperty(t *testing.T) {
	data, err := ExportSARIF(nil, true)
	if err != nil {
		t.Fatalf("ExportSARIF: %v", err)
	}
	if !strings.Contains(string(data), `"truncated":true`) {
		t.Fatalf("missing truncated property: %s", data)
	}
}
