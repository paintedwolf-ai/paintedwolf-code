package advisory_test

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/advisory"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNormalizeCVE(t *testing.T) {
	if got := advisory.NormalizeCVE(" cve-2024-1234 "); got != "CVE-2024-1234" {
		t.Fatalf("got %q", got)
	}
	if got := advisory.NormalizeCVE("CVE-2024"); got != "" {
		t.Fatalf("malformed accepted: %q", got)
	}
}

func TestNormalizeGHSAKeepsGitHubSpelling(t *testing.T) {
	if got := advisory.NormalizeGHSA("ghsa-ABCD-1234-efgh"); got != "GHSA-abcd-1234-efgh" {
		t.Fatalf("got %q", got)
	}
	if got := advisory.NormalizeGHSA("GHSA-abcd-1234"); got != "" {
		t.Fatalf("malformed accepted: %q", got)
	}
}

func TestCanonicalIDPrefersCVEThenGHSAThenDatabaseID(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
		want string
	}{
		{"cve wins", []string{"GO-2020-0001", "GHSA-6vm3-jj99-7229", "CVE-2020-36567"}, "CVE-2020-36567"},
		{"lowest cve", []string{"CVE-2024-2", "CVE-2024-1"}, "CVE-2024-1"},
		{"ghsa without cve", []string{"PYSEC-2024-1", "ghsa-6vm3-jj99-7229"}, "GHSA-6vm3-jj99-7229"},
		{"database id alone", []string{"MAL-2025-2544"}, "MAL-2025-2544"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := advisory.BuildAdvisoryRef(tc.ids[0], tc.ids[1:]...)
			if ref == nil || ref.OSVID != tc.want {
				t.Fatalf("canonical = %#v, want %q", ref, tc.want)
			}
			if got := advisory.RuleID(ref); got != "osv:"+tc.want {
				t.Fatalf("rule = %q", got)
			}
		})
	}
}

func TestCanonicalIsIndependentOfWhichRecordMatched(t *testing.T) {
	goRecord := advisory.BuildAdvisoryRef("GO-2020-0001", "CVE-2020-36567", "GHSA-6vm3-jj99-7229")
	ghsaRecord := advisory.BuildAdvisoryRef("GHSA-6vm3-jj99-7229", "CVE-2020-36567", "GO-2020-0001")
	if !reflect.DeepEqual(goRecord, ghsaRecord) {
		t.Fatalf("identity differs by matched record:\n%#v\n%#v", goRecord, ghsaRecord)
	}
	want := &api.AdvisoryRef{
		OSVID:   "CVE-2020-36567",
		CVEIDs:  []string{"CVE-2020-36567"},
		GHSAIDs: []string{"GHSA-6vm3-jj99-7229"},
		Aliases: []string{"GO-2020-0001"},
	}
	if !reflect.DeepEqual(goRecord, want) {
		t.Fatalf("identity = %#v, want %#v", goRecord, want)
	}
}

func TestNormalizeRefilesIDsByFamilyAndIsIdempotent(t *testing.T) {
	ref := &api.AdvisoryRef{OSVID: "GHSA-6VM3-JJ99-7229", CVEIDs: []string{"GO-2020-0001"}, Aliases: []string{"cve-2020-36567", "cve-2020-36567"}}
	advisory.Normalize(ref)
	first := *ref
	advisory.Normalize(ref)
	if !reflect.DeepEqual(first, *ref) {
		t.Fatalf("not idempotent: %#v vs %#v", first, *ref)
	}
	want := api.AdvisoryRef{OSVID: "CVE-2020-36567", CVEIDs: []string{"CVE-2020-36567"}, GHSAIDs: []string{"GHSA-6vm3-jj99-7229"}, Aliases: []string{"GO-2020-0001"}}
	if !reflect.DeepEqual(*ref, want) {
		t.Fatalf("normalized = %#v, want %#v", *ref, want)
	}
	if got := advisory.IDs(ref); !reflect.DeepEqual(got, []string{"CVE-2020-36567", "GHSA-6vm3-jj99-7229", "GO-2020-0001"}) {
		t.Fatalf("ids = %#v", got)
	}
}

func TestBuildAdvisoryRefWithoutIdentityIsNil(t *testing.T) {
	if ref := advisory.BuildAdvisoryRef(" ", ""); ref != nil {
		t.Fatalf("ref = %#v", ref)
	}
	if got := advisory.RuleID(nil); got != "" {
		t.Fatalf("nil rule = %q", got)
	}
	if got := advisory.IDs(nil); got != nil {
		t.Fatalf("nil ids = %#v", got)
	}
}
