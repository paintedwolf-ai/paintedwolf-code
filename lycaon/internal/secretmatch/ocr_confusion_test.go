package secretmatch

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledOCRConfusionCatalogValidation(t *testing.T) {
	cat, err := bundledOCRConfusions()
	testutil.FailErr(t, "load bundled ocr confusions", err)
	for _, pair := range [][2]rune{
		{'0', 'O'},
		{'1', 'I'},
		{'1', 'l'},
		{'I', 'l'},
		{'5', 'S'},
		{'7', '/'},
	} {
		a, b := pair[0], pair[1]
		if cat.canonical[a] == 0 || cat.canonical[a] != cat.canonical[b] {
			t.Fatalf("expected %q and %q in same confusion class, got canonical %q vs %q", a, b, cat.canonical[a], cat.canonical[b])
		}
	}

	invalidCases := []struct {
		name string
		yaml string
	}{
		{
			name: "unsupported version",
			yaml: "version: 2\nclasses:\n  - glyphs: [\"0\", \"O\"]\n    reason: oval\n",
		},
		{
			name: "empty classes",
			yaml: "version: 1\nclasses: []\n",
		},
		{
			name: "missing reason",
			yaml: "version: 1\nclasses:\n  - glyphs: [\"0\", \"O\"]\n    reason: \"\"\n",
		},
		{
			name: "singleton class",
			yaml: "version: 1\nclasses:\n  - glyphs: [\"0\"]\n    reason: single\n",
		},
		{
			name: "multi-rune glyph",
			yaml: "version: 1\nclasses:\n  - glyphs: [\"00\", \"O\"]\n    reason: multi\n",
		},
		{
			name: "whitespace glyph",
			yaml: "version: 1\nclasses:\n  - glyphs: [\" \", \"O\"]\n    reason: space\n",
		},
		{
			name: "duplicate glyph across classes",
			yaml: "version: 1\nclasses:\n  - glyphs: [\"0\", \"O\"]\n    reason: first\n  - glyphs: [\"O\", \"Q\"]\n    reason: second\n",
		},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseOCRConfusions([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

type ocrSecretCase struct {
	name       string
	confused   string
	wantRuleID string
	strictMiss bool
}

// twentyFourOCRConfusedSecrets is the 24-secret corpus (12 AKIA + 12 ghp_)
// exercising 0/O, 1/I/l, 5/S, and 7// confusions in prefixes and token bodies.
var twentyFourOCRConfusedSecrets = []ocrSecretCase{
	// 12 AWS access key IDs (AKIA...) with prefix and body confusions.
	{name: "aws-01-prefix-I-to-1", confused: "AK1AQ9Z2M4N8P3R6T5V7", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-02-prefix-I-to-l", confused: "AKlAW3E6R4T2Y7U5I6O3", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-03-body-O-to-0", confused: "AKIAQ2W3E4R5T6Y7U602", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-04-body-I-to-1", confused: "AKIAZ2X3C4V5B6N7M613", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-05-body-I-to-l", confused: "AKIAP7O6I5U4Y3T2R3l6", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-06-body-7-to-slash", confused: "AKIAL2K3J4H5G6F/D4S5", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-07-trailing-7-to-slash", confused: "AKIAM7N6B5V4C3X2Z3L/", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-08-body-S-to-5", confused: "AKIAQ7W2E5R3T6Y4U257", wantRuleID: "gitleaks:aws-access-token", strictMiss: false},
	{name: "aws-09-prefix-1-body-0-slash", confused: "AK1AD6F4G2H7J3K502/", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-10-prefix-l-body-1-0", confused: "AKlAF3G5H7J2K4L61057", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-11-multi-O-I-S-7", confused: "AKIA01l5/2W3E4R5T6Y7", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},
	{name: "aws-12-prefix-1-multi-body", confused: "AK1A0O1Il5S/7Q2W3E4R", wantRuleID: "gitleaks:aws-access-token", strictMiss: true},

	// 12 GitHub personal access tokens (ghp_...) with 0/O, 1/I/l, 5/S, 7// confusions.
	{name: "ghp-01-body-7-to-slash", confused: "ghp_A1b2C3d4E5f6G/h8I9j0K1l2M3n4O5p6Q7r8", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-02-multi-7-to-slash", confused: "ghp_Z9y8X/w6V5u4T3s2R1q0P9o8N/m6L5k4J3i2", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-03-trailing-7-to-slash", confused: "ghp_Q2w3E4r5T6y7U8i9O0p1A2s3D4f5G6h7J8k/", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-04-leading-7-to-slash", confused: "ghp_/bC2dE3fG4hI5jK6lM7nO8pQ9rS0tU1vW2xY", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-05-0-to-O-and-slash", confused: "ghp_mN8bV/cX6zL5kJ4hG3fD2sA1pOIUyTrEwQqW", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-06-1-I-l-and-slash", confused: "ghp_1IlI1l/9aB8cD7eF6gH5iJ4kL3mN2oP1qR0s", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-07-5-to-S-and-slash", confused: "ghp_S5S5aB2cD3eF4gH6iJ/kL8mN9oP0qR1sT2uV", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-08-combined-classes-a", confused: "ghp_O0I1l5S/7aBcDeFgHiJkLmNoPqRsTuVwXyZ2", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-09-combined-classes-b", confused: "ghp_k9J8h/G6f5D4s3A2p1O0i9U8y/T6r5E4w3Q2", wantRuleID: "gitleaks:github-pat", strictMiss: true},
	{name: "ghp-10-0-O-swap", confused: "ghp_R4t5Y6u7I8o9P0a1S2d3F4g5H6j7K8l9Z0xO", wantRuleID: "gitleaks:github-pat", strictMiss: false},
	{name: "ghp-11-1-I-l-swap", confused: "ghp_V3b4N5m6Q7w8E9r0T1y2U3i4O5p6A7s8D9fl", wantRuleID: "gitleaks:github-pat", strictMiss: false},
	{name: "ghp-12-5-S-swap", confused: "ghp_C2v3B4n5M6q7W8e9R0t1Y2u3I4o5P6a7S8dS", wantRuleID: "gitleaks:github-pat", strictMiss: false},
}

func TestScreenOCRDetects24SecretCorpusAndPreservesOffsets(t *testing.T) {
	matcher := loadBundled(t)
	detected := 0
	for _, tc := range twentyFourOCRConfusedSecrets {
		prefix := "Screenshot line: export TOKEN="
		suffix := " # end of line"
		line := prefix + tc.confused + suffix
		wantStart := utf8.RuneCountInString(prefix)
		wantEnd := wantStart + utf8.RuneCountInString(tc.confused)

		hits := matcher.ScreenOCR(line)
		if hasRule(hits, tc.wantRuleID) {
			detected++
		} else {
			t.Errorf("%s (%q): ScreenOCR missed %s; got %#v", tc.name, tc.confused, tc.wantRuleID, hits)
			continue
		}
		var found Match
		for _, h := range hits {
			if h.RuleID == tc.wantRuleID {
				found = h
				break
			}
		}
		if found.Start != wantStart || found.End != wantEnd {
			t.Errorf("%s: span = [%d, %d), want [%d, %d)", tc.name, found.Start, found.End, wantStart, wantEnd)
		}
		redacted := matcher.RedactOCRString(context.Background(), line)
		wantRedacted := prefix + RedactedMarker + suffix
		if redacted != wantRedacted {
			t.Errorf("%s: RedactOCRString = %q, want %q", tc.name, redacted, wantRedacted)
		}
		if tc.strictMiss {
			if strictHits := matcher.Screen(line); hasRule(strictHits, tc.wantRuleID) {
				t.Errorf("%s: strict Screen unexpectedly matched OCR-confused token %q", tc.name, tc.confused)
			}
		}
	}
	rate := float64(detected) / float64(len(twentyFourOCRConfusedSecrets))
	if rate < 0.90 {
		t.Fatalf("24-secret OCR detection rate = %.1f%% (%d/%d), want >= 90%%", rate*100, detected, len(twentyFourOCRConfusedSecrets))
	}
}

func TestScreenOCROtherFamiliesAndHarvestedValues(t *testing.T) {
	matcher := loadBundled(t)
	cases := []struct {
		name   string
		input  string
		ruleID string
	}{
		{
			name:   "AWS temporary key ASIA with S->5 and I->1",
			input:  "export AWS_SESSION_TOKEN=A51AQ9Z2M4N8P3R6T5V7",
			ruleID: "gitleaks:aws-access-token",
		},
		{
			name:   "GCP API key AIza with I->1 and 7->slash",
			input:  "google_key = A1zaSyD9mK2pL4nQ6rT8vW0xY3zB5cF/hJ1kM3",
			ruleID: "gitleaks:gcp-api-key",
		},
		{
			name:   "Anthropic key with api03->ap1O3",
			input:  "ANTHROPIC_API_KEY=sk-ant-ap1O3-aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789aBcDeFgHiJkLmNoPqRsTu-aBcDeFgH",
			ruleID: "gitleaks:anthropic-api-key",
		},
		{
			name:   "OpenRouter key with v1->vI and hex O/I/l/S/slash",
			input:  "OPENROUTER_API_KEY=sk-0r-vI-a1b2c3d4e5f6O718I9l0S1/2a3b4c5d6e7f80910111213141516171819202122",
			ruleID: "openrouter-api-key",
		},
		{
			name:   "OpenAI key with T3BlbkFJ->T3B1bkFJ",
			input:  "OPENAI_API_KEY=sk-aBcDeFgHiJkLmNoPqRsTT3B1bkFJaBcDeFgHiJkLmNoPqRsT",
			ruleID: "gitleaks:openai-api-key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := matcher.ScreenOCR(tc.input)
			if !hasRule(hits, tc.ruleID) {
				t.Fatalf("ScreenOCR(%q) = %#v, want %s", tc.input, hits, tc.ruleID)
			}
		})
	}

	// Harvested and managed values tolerate OCR glyph confusions while preserving offsets.
	fp, err := NewFingerprinter([]byte("0123456789abcdef0123456789abcdef"))
	testutil.FailErr(t, "fingerprinter", err)
	matcher.SetFingerprinter(fp)
	matcher.SetHarvestSource(func(context.Context) []HarvestedValue {
		return []HarvestedValue{{
			Name:        "CUSTOM_SECRET",
			Container:   ".env",
			Secret:      "myCustomSecret0157Token",
			Fingerprint: fp.Fingerprint("myCustomSecret0157Token"),
			RuleID:      HarvestRuleID,
			Title:       "Container secret",
			Source:      SourceContainerHarvest,
		}}
	})
	harvestLine := "env dump: myCu5tom5ecretOIl/Token here"
	hHits := matcher.ScreenOCR(harvestLine)
	if !hasRule(hHits, HarvestRuleID) {
		t.Fatalf("ScreenOCR missed harvested value with OCR confusions: %#v", hHits)
	}
	if got := matcher.RedactOCRString(context.Background(), harvestLine); got != "env dump: [REDACTED] here" {
		t.Fatalf("RedactOCRString(harvest) = %q", got)
	}
}

func TestScreenOCRSuppressesConfusedPlaceholdersAndNonSecrets(t *testing.T) {
	matcher := loadBundled(t)
	placeholders := []string{
		"AKIAIOSFODNN7EXAMPLE",
		"AK1AI0SF0DNN/EXAMP1E",
		"AKlAI05F0DNN7EXAMPLE",
		"A51AI0SF0DNN/EXAMP1E",
		"ghp_00000000000O000000000000000000000000",
		"AIzaSyA-EXAMP1E-KEY-000000000000000000",
	}
	for _, p := range placeholders {
		if hits := matcher.ScreenOCR(p); len(hits) != 0 {
			t.Errorf("placeholder %q matched in ScreenOCR: %#v", p, hits)
		}
	}

	nonSecretScreenshotLines := []string{
		"File: /Users/developer/workspace/paintedwolf/lycaon/internal/visualscreen/scanner.go",
		"URL: https://github.com/paintedwolf/lycaon/pull/785/files#diff-0123456789abcdef",
		"commit 01bb82ccac9f4e3d2a1b0c9d8e7f6a5b4c3d2e1f (HEAD -> main, origin/main)",
		"UUID: 550e8400-e29b-41d4-a716-446655440000",
		"go test -v ./internal/secretmatch/... ./internal/visualscreen/...",
		"const RejectImageDimension = 8192",
		"func (g *Gate) Screen(ctx context.Context, in VisualScreenInput) (*VisualScreenOutcome, error)",
		"drwxr-xr-x  14 user  staff   448B Oct 31 17:00 lycaon-den/src-tauri/target/release",
		"Error: failed to connect to localhost:8080/api/v1/healthz: connection refused",
		"export PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin",
	}
	for _, line := range nonSecretScreenshotLines {
		if hits := matcher.ScreenOCR(line); len(hits) != 0 {
			t.Errorf("non-secret screenshot line %q produced false positive: %#v", line, hits)
		}
	}
	if hits := matcher.ScreenOCR(strings.Join(nonSecretScreenshotLines, "\n")); len(hits) != 0 {
		t.Errorf("combined non-secret screenshot text produced false positives: %#v", hits)
	}
}
