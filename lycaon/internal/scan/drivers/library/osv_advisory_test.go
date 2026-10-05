package library

import (
	"reflect"
	"testing"

	"github.com/google/osv-scalibr/extractor"
	"github.com/google/osv-scalibr/inventory"
	"github.com/lycaon/lycaon/internal/advisory/severity"
	"github.com/lycaon/lycaon/pkg/api"
	osv "github.com/ossf/osv-schema/bindings/go/osvschema"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestAdvisorySeverityPreservesUnknownAndPublishedScores(t *testing.T) {
	for _, tc := range []struct {
		name, vector string
		kind         osv.Severity_Type
		want         api.FindingLevel
	}{
		{"absent", "", osv.Severity_CVSS_V3, api.FindingLevelUnknown},
		{"malformed", "not a vector", osv.Severity_CVSS_V3, api.FindingLevelUnknown},
		{"critical", "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", osv.Severity_CVSS_V3, api.FindingLevelCritical},
		{"medium", "CVSS:3.0/AV:N/AC:H/PR:N/UI:R/S:U/C:L/I:L/A:N", osv.Severity_CVSS_V3, api.FindingLevelMedium},
		{"zero", "AV:L/AC:H/Au:M/C:N/I:N/A:N", osv.Severity_CVSS_V2, api.FindingLevelInfo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := &api.AdvisoryRef{}
			got := advisorySeverity(ref, []*osv.Severity{{Type: tc.kind, Score: tc.vector}})
			if got != tc.want {
				t.Fatalf("severity = %s, want %s", got, tc.want)
			}
			if tc.vector != "" && (len(ref.CVSS) != 1 || ref.CVSS[0].Vector != tc.vector) {
				t.Fatalf("vector not retained: %#v", ref)
			}
		})
	}
}

func goPackage(name string) *extractor.Package {
	return &extractor.Package{Name: name, Version: "1.0.0", PURLType: "golang", Location: extractor.LocationFromPath("/proj/go.mod")}
}

func TestPackageAdvisoryPreservesAliasesAndMatchingFixes(t *testing.T) {
	v := &osv.Vulnerability{Id: "GHSA-2345-6789-cfgh", Aliases: []string{"CVE-2026-12345", "CVE-2026-12345", "GO-2026-0001"},
		DatabaseSpecific: &structpb.Struct{Fields: map[string]*structpb.Value{"severity": structpb.NewStringValue("MODERATE")}},
		Affected: []*osv.Affected{
			{Package: &osv.Package{Name: "example.org/a", Ecosystem: "Go"}, Ranges: []*osv.Range{{Type: osv.Range_SEMVER, Events: []*osv.Event{{Fixed: "1.2.3"}}}}},
			{Package: &osv.Package{Name: "example.org/b", Ecosystem: "Go"}, Ranges: []*osv.Range{{Type: osv.Range_SEMVER, Events: []*osv.Event{{Fixed: "9.9.9"}}}}},
		}, References: []*osv.Reference{{Url: "https://example.org/advisory"}},
	}
	ref, level := packageAdvisory(&inventory.PackageVuln{Vulnerability: v, Package: goPackage("example.org/a")})
	if level != api.FindingLevelMedium || ref.SeveritySource != severitySourceDatabase || ref.Kind != api.AdvisoryKindVulnerability {
		t.Fatalf("severity = %s / %s / %s", level, ref.SeveritySource, ref.Kind)
	}
	if ref.OSVID != "CVE-2026-12345" || !reflect.DeepEqual(ref.GHSAIDs, []string{"GHSA-2345-6789-cfgh"}) || !reflect.DeepEqual(ref.Aliases, []string{"GO-2026-0001"}) {
		t.Fatalf("identity = %#v", ref)
	}
	if !reflect.DeepEqual(ref.FixedVersions, []string{"1.2.3"}) || len(ref.CVEIDs) != 1 || len(ref.URLs) != 1 {
		t.Fatalf("metadata = %#v", ref)
	}
}

func TestPackageAdvisoryClassifiesMalwareReportsAsCritical(t *testing.T) {
	origins, err := structpb.NewList([]any{map[string]any{"source": "google-open-source-security", "sha256": "ae6b"}})
	if err != nil {
		t.Fatalf("origins: %v", err)
	}
	v := &osv.Vulnerability{Id: "MAL-2025-2544", Summary: "Malicious code in example.org/hypert (Go)",
		DatabaseSpecific: &structpb.Struct{Fields: map[string]*structpb.Value{maliciousPackageOriginsField: structpb.NewListValue(origins)}},
	}
	ref, level := packageAdvisory(&inventory.PackageVuln{Vulnerability: v, Package: goPackage("example.org/hypert")})
	if level != api.FindingLevelCritical || ref.Kind != api.AdvisoryKindMaliciousPackage || ref.SeveritySource != severitySourceMaliciousPackage {
		t.Fatalf("malware report = %s / %s / %s", level, ref.Kind, ref.SeveritySource)
	}
	if ref.OSVID != "MAL-2025-2544" || !reflect.DeepEqual(ref.Aliases, []string{"MAL-2025-2544"}) {
		t.Fatalf("identity = %#v", ref)
	}

	empty := &osv.Vulnerability{Id: "GO-2026-0002", DatabaseSpecific: &structpb.Struct{Fields: map[string]*structpb.Value{
		maliciousPackageOriginsField: structpb.NewListValue(&structpb.ListValue{}),
	}}}
	if ref, level := packageAdvisory(&inventory.PackageVuln{Vulnerability: empty, Package: goPackage("example.org/a")}); level != api.FindingLevelUnknown || ref.Kind != api.AdvisoryKindVulnerability {
		t.Fatalf("empty origins list classified as malware: %s / %s", level, ref.Kind)
	}
}

func TestMapPackageVulnsFoldsTwinRecordsAndKeepsUnrelatedApart(t *testing.T) {
	pkg := goPackage("example.org/a")
	goRecord := &osv.Vulnerability{Id: "GO-2020-0001", Aliases: []string{"CVE-2020-36567", "GHSA-6vm3-jj99-7229"}, Summary: "Log injection in example.org/a"}
	ghsaRecord := &osv.Vulnerability{Id: "GHSA-6vm3-jj99-7229", Aliases: []string{"CVE-2020-36567", "GO-2020-0001"}, Summary: "example.org/a vulnerable to log injection",
		Severity: []*osv.Severity{{Type: osv.Severity_CVSS_V3, Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:H/A:N"}}}
	other := &osv.Vulnerability{Id: "GO-2021-0002", Aliases: []string{"CVE-2021-1"}, Summary: "Another bug in example.org/a"}
	findings := mapPackageVulns([]*inventory.PackageVuln{
		{Vulnerability: goRecord, Package: pkg},
		{Vulnerability: ghsaRecord, Package: pkg},
		{Vulnerability: other, Package: pkg},
	}, "/proj", "lycaon-sca")
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want twins folded and the unrelated record kept", len(findings))
	}
	twin := findings[0]
	if twin.RuleID != "osv:CVE-2020-36567" || twin.Level != api.FindingLevelHigh || twin.Properties.Lycaon.Advisory.SeveritySource != severitySourceCVSS {
		t.Fatalf("folded twin = %s %s %s", twin.RuleID, twin.Level, twin.Properties.Lycaon.Advisory.SeveritySource)
	}
	if !reflect.DeepEqual(twin.Locations, []api.SecurityFindingLocation{{URI: "go.mod"}}) {
		t.Fatalf("locations = %#v", twin.Locations)
	}
	if findings[1].RuleID != "osv:CVE-2021-1" || findings[1].Level != api.FindingLevelUnknown {
		t.Fatalf("unrelated record = %s %s", findings[1].RuleID, findings[1].Level)
	}
}

func TestPackageAdvisoryResolvesSeverityViaAlias(t *testing.T) {
	v := &osv.Vulnerability{
		Id:      "GO-2026-4599",
		Aliases: []string{"CVE-2026-27137"},
		Summary: "Incorrect enforcement of email constraints in crypto/x509",
	}
	ref, level := packageAdvisory(&inventory.PackageVuln{
		Vulnerability: v,
		Package:       goPackage("stdlib"),
	})
	if level != api.FindingLevelHigh {
		t.Fatalf("level = %s, want high from nvd.cvss", level)
	}
	if ref.SeveritySource != severity.SourceNVDCVSS {
		t.Fatalf("source = %s, want %s", ref.SeveritySource, severity.SourceNVDCVSS)
	}
	if len(ref.CVSS) == 0 || ref.CVSS[0].Score == nil || *ref.CVSS[0].Score != 7.5 {
		t.Fatalf("expected cvss with score 7.5, got %#v", ref.CVSS)
	}
}
