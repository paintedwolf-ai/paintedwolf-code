package severity_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/advisory/severity"
	"github.com/lycaon/lycaon/pkg/api"
)

func defaultResolver(t *testing.T) severity.Resolver {
	t.Helper()
	resolver, err := severity.Default()
	if err != nil {
		t.Fatalf("load bundled severity catalog: %v", err)
	}
	return resolver
}

func TestParseCatalogRejectsInconsistentRows(t *testing.T) {
	for name, row := range map[string]string{
		"lower-case id":  `{"id":"cve-2099-0001","type":"CVSS_V3","vector":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N","source":"nvd.cvss"}`,
		"type mismatch":  `{"id":"CVE-2099-0001","type":"CVSS_V4","vector":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N","source":"nvd.cvss"}`,
		"invalid vector": `{"id":"CVE-2099-0001","type":"CVSS_V3","vector":"CVSS:3.1/AV:X","source":"nvd.cvss"}`,
		"unknown source": `{"id":"CVE-2099-0001","type":"CVSS_V3","vector":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N","source":"guess"}`,
		"stored score":   `{"id":"CVE-2099-0001","type":"CVSS_V3","vector":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N","source":"nvd.cvss","score":9.9}`,
	} {
		if _, err := severity.ParseCatalog([]byte("[" + row + "]")); err == nil {
			t.Errorf("%s: catalog row accepted", name)
		}
	}
	row := `{"id":"CVE-2099-0001","type":"CVSS_V3","vector":"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N","source":"nvd.cvss"}`
	if _, err := severity.ParseCatalog([]byte("[" + row + "," + row + "]")); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate rows = %v", err)
	}
	entries, err := severity.ParseCatalog([]byte("[" + row + "]"))
	if err != nil || len(entries) != 1 || entries[0].Score != 5.3 {
		t.Fatalf("parsed = %+v err=%v", entries, err)
	}
}

func TestResolverResolvesKnownCVEs(t *testing.T) {
	ref := &api.AdvisoryRef{
		OSVID:   "GO-2026-4599",
		CVEIDs:  []string{"CVE-2026-27137"},
		Aliases: []string{"GO-2026-4599"},
	}

	cvss, source, level, ok := defaultResolver(t).Resolve(ref)
	if !ok {
		t.Fatalf("expected CVE-2026-27137 to resolve")
	}
	if source != severity.SourceNVDCVSS {
		t.Fatalf("source = %q, want %q", source, severity.SourceNVDCVSS)
	}
	if level != api.FindingLevelHigh {
		t.Fatalf("level = %q, want %q", level, api.FindingLevelHigh)
	}
	if cvss == nil || cvss.Score == nil || *cvss.Score != 7.5 {
		t.Fatalf("cvss = %#v, want score 7.5", cvss)
	}
}

func TestResolverResolvesCriticalAndMedium(t *testing.T) {
	cases := []struct {
		cve       string
		wantLevel api.FindingLevel
	}{
		{"CVE-2026-33810", api.FindingLevelCritical},
		{"CVE-2026-39822", api.FindingLevelCritical},
		{"CVE-2026-25679", api.FindingLevelMedium},
		{"CVE-2026-27142", api.FindingLevelMedium},
	}
	for _, tc := range cases {
		ref := &api.AdvisoryRef{
			OSVID:  tc.cve,
			CVEIDs: []string{tc.cve},
		}
		_, _, level, ok := defaultResolver(t).Resolve(ref)
		if !ok {
			t.Errorf("expected %s to resolve", tc.cve)
		}
		if level != tc.wantLevel {
			t.Errorf("%s: got level %q, want %q", tc.cve, level, tc.wantLevel)
		}
	}
}

func TestResolverFallsBackOnUnknown(t *testing.T) {
	ref := &api.AdvisoryRef{
		OSVID:   "GO-9999-0000",
		Aliases: []string{"GO-9999-0000"},
	}
	cvss, source, level, ok := defaultResolver(t).Resolve(ref)
	if ok || cvss != nil || source != "" || level != api.FindingLevelUnknown {
		t.Fatalf("expected uncataloged advisory to fail resolution, got ok=%v level=%s", ok, level)
	}
}

func TestResolverCustomEntries(t *testing.T) {
	custom := severity.NewResolver(severity.Entry{
		ID:     "GHSA-1111-2222-3333",
		Type:   "CVSS_V3",
		Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
		Score:  9.8,
		Source: severity.SourceGHSACVSS,
	})

	ref := &api.AdvisoryRef{
		OSVID:   "GHSA-1111-2222-3333",
		GHSAIDs: []string{"GHSA-1111-2222-3333"},
	}
	cvss, source, level, ok := custom.Resolve(ref)
	if !ok || level != api.FindingLevelCritical || source != severity.SourceGHSACVSS {
		t.Fatalf("custom resolution failed: ok=%v level=%s source=%s", ok, level, source)
	}
	if cvss.Vector != "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H" {
		t.Fatalf("unexpected vector: %s", cvss.Vector)
	}
}
