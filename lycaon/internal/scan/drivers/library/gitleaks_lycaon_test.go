package library_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/drivers/library"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGitleaksScannerSkipsLycaonSandboxes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.go"), []byte(`api_key = "AKIAQYJK5TXV4NZR7SGB"`), 0o644); err != nil {
		testutil.FailErr(t, "write real.go", err)
	}
	sandbox := filepath.Join(dir, settingsoverlay.DirName(), "worktrees", "job-a")
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		testutil.FailErr(t, "mkdir sandbox", err)
	}
	if err := os.WriteFile(filepath.Join(sandbox, "leak.go"), []byte(`password = "super-secret-token-12345"`), 0o644); err != nil {
		testutil.FailErr(t, "write leak.go", err)
	}

	scanner := newGitleaksScanner(t)
	res, err := scanner.Run(context.Background(), scan.ScanRequest{ProjectDir: dir})
	testutil.FailErr(t, "GitleaksScanner.Run", err)
	foundReal := false
	for _, f := range res.Findings {
		uri := scanfindings.PrimaryURI(f)
		if f.Tool.DriverID != "test-secrets" {
			t.Fatalf("finding driver id = %q, want scanner id", f.Tool.DriverID)
		}
		if filepath.Base(filepath.Dir(uri)) == "job-a" || filepath.ToSlash(uri) == settingsoverlay.DirName()+"/worktrees/job-a/leak.go" {
			t.Fatalf("finding under .paintedwolf: %+v", f)
		}
		if filepath.Base(uri) == "real.go" && f.RuleID == "gitleaks:aws-access-token" {
			foundReal = true
		}
	}
	if !foundReal {
		t.Fatal("expected AWS finding outside overlay")
	}
}

func TestGitleaksScannerUsesSharedProviderPlaceholderAllowlists(t *testing.T) {
	dir := t.TempDir()
	placeholders := strings.Join([]string{
		`AWS_ACCESS_KEY_ID="AKIAIOSFODNN7EXAMPLE"`,
		`AWS_SECRET_ACCESS_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"`,
		`AZURE_OPENAI_API_KEY="REPLACE_WITH_YOUR_KEY_VALUE_HERE"`,
		`BRAVE_SEARCH_API_KEY="your_actual_api_key_here"`,
		`FIREWORKS_API_KEY="fw_your_api_key_here"`,
		`KAGI_API_KEY="YOUR_TOKEN_HERE"`,
		`TAVILY_API_KEY="tvly-YOUR_API_KEY"`,
		`TOGETHER_API_KEY="your_api_key"`,
		`OPENROUTER_API_KEY="<OPENROUTER_API_KEY>"`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "placeholders.env"), []byte(placeholders), 0o644); err != nil {
		testutil.FailErr(t, "write placeholders", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "real.env"),
		[]byte("TAVILY_API_KEY=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\n"),
		0o644,
	); err != nil {
		testutil.FailErr(t, "write real provider key", err)
	}

	res, err := newGitleaksScanner(t).Run(t.Context(), scan.ScanRequest{ProjectDir: dir})
	testutil.FailErr(t, "GitleaksScanner.Run", err)
	foundReal := false
	for _, finding := range res.Findings {
		if filepath.Base(scanfindings.PrimaryURI(finding)) == "placeholders.env" {
			t.Errorf("published placeholder produced finding: %+v", finding)
		}
		if filepath.Base(scanfindings.PrimaryURI(finding)) == "real.env" && finding.RuleID == "gitleaks:tavily-api-key" {
			foundReal = true
		}
	}
	if !foundReal {
		t.Fatalf("shared provider rule did not detect realistic Tavily key: %+v", res.Findings)
	}
}

func newGitleaksScanner(t *testing.T) *library.GitleaksScanner {
	t.Helper()
	scanner, err := library.NewGitleaksScanner(library.GitleaksOptions{ID: "test-secrets"})
	testutil.FailErr(t, "NewGitleaksScanner", err)
	return scanner
}
