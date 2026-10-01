package library

import (
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/zricethezav/gitleaks/v8/report"
)

func TestFilterGitleaksFindingsPrefersExistingRuleOverVendorDuplicate(t *testing.T) {
	profile, err := secretmatch.BuildScannerProfile(secretmatch.Bundled())
	testutil.FailErr(t, "BuildScannerProfile", err)
	const secret = "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"
	line := "GROQ_API_KEY=" + secret
	raw := []report.Finding{
		{RuleID: "kingfisher.groq.1", File: ".env", StartLine: 3, Line: line, Secret: secret},
		{RuleID: "groq-api-key", File: ".env", StartLine: 3, Line: line, Secret: secret},
	}
	got := filterGitleaksFindings(profile, raw)
	if len(got) != 1 || got[0].RuleID != "groq-api-key" {
		t.Fatalf("filtered findings = %#v", got)
	}
	if raw[0].Secret != "" || raw[0].Line != "" {
		t.Fatal("discarded vendor finding retained secret material")
	}
}
