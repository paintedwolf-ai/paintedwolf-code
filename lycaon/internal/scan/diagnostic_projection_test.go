package scan

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/observability"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

const probeSecret = "ghp_A9fK2mQ7zX4bR1nT6yW8pL3vC5dH0jS2gU7e"

func TestSecretScannerReportIsWithheldFromScanErrors(t *testing.T) {
	report := `{"runs":[{"results":[{"locations":[{"physicalLocation":{"region":{"snippet":{"text":"` +
		probeSecret + `"}}}}]}]}]}`

	for _, tc := range []struct {
		name    string
		subject DiagnosticSubject
	}{
		{
			name:    "secrets scope kind",
			subject: DiagnosticSubject{Stream: DiagnosticReport, ScopeKind: scancatalog.ScopeSecrets},
		},
		{
			name: "secret category",
			subject: DiagnosticSubject{
				Stream:     DiagnosticReport,
				Categories: []api.ScanCategory{api.ScanCategorySecret, api.ScanCategorySecurity},
			},
		},
		{
			name:    "console stream of a secret scanner",
			subject: DiagnosticSubject{Stream: DiagnosticConsole, ScopeKind: scancatalog.ScopeSecrets},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DiagnosticExcerpt(tc.subject, []byte(report))
			if strings.Contains(got, probeSecret) {
				t.Fatalf("scan error excerpt disclosed the reported secret: %q", got)
			}
			if got != diagnosticWithheldMarker {
				t.Fatalf("want an explicit withheld marker, got %q", got)
			}
		})
	}
}

func TestNonSecretScannerOutputIsScreenedBeforeItIsBounded(t *testing.T) {
	observability.SetCaptureRedactor(func(s string) string {
		return strings.ReplaceAll(s, probeSecret, "[REDACTED]")
	})
	t.Cleanup(func() { observability.SetCaptureRedactor(nil) })

	// Place the value across the byte cap.
	head := strings.Repeat("x", diagnosticExcerptBytes-len(probeSecret)/2)
	raw := head + probeSecret + strings.Repeat("y", 4096)

	subject := DiagnosticSubject{
		Stream:     DiagnosticReport,
		ScopeKind:  scancatalog.ScopeKind("sast"),
		Categories: []api.ScanCategory{api.ScanCategorySAST},
	}
	got := DiagnosticExcerpt(subject, []byte(raw))
	if got == "" {
		t.Fatal("a non-secret scanner should still report a diagnostic excerpt")
	}
	if strings.Contains(got, probeSecret) {
		t.Fatalf("excerpt disclosed the secret: %q", got)
	}
	// Check fragments large enough to match.
	if fragment := probeSecret[:len(probeSecret)/2]; strings.Contains(got, fragment) {
		t.Fatalf("excerpt kept a fragment of the secret, so the cap ran before screening: %q", got)
	}
	if len(got) > diagnosticExcerptBytes {
		t.Fatalf("excerpt exceeds the %d byte bound: %d", diagnosticExcerptBytes, len(got))
	}
}

func TestDiagnosticExcerptKeepsTheEndOfConsoleOutput(t *testing.T) {
	observability.SetCaptureRedactor(nil)
	raw := strings.Repeat("a", diagnosticExcerptBytes) + "the failure that matters"

	subject := DiagnosticSubject{Stream: DiagnosticConsole, ScopeKind: scancatalog.ScopeKind("sast")}
	got := DiagnosticExcerpt(subject, []byte(raw))
	if !strings.HasSuffix(got, "the failure that matters") {
		t.Fatalf("console excerpt should keep its tail, got %q", got)
	}
}

func TestDiagnosticSuffixIsEmptyWithoutOutput(t *testing.T) {
	subject := DiagnosticSubject{Stream: DiagnosticConsole}
	if got := DiagnosticSuffix("stderr", subject, []byte("   \n ")); got != "" {
		t.Fatalf("blank output should produce no suffix, got %q", got)
	}
}
