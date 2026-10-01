package scan

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestConsoleExcerptBoundsOutputAndWithholdsSecrets(t *testing.T) {
	t.Parallel()
	sast := DiagnosticSubject{Stream: DiagnosticConsole, Categories: []api.ScanCategory{api.ScanCategorySAST}}

	// Secret-bearing output is withheld whole, markers included.
	secrets := DiagnosticSubject{Stream: DiagnosticConsole, Categories: []api.ScanCategory{api.ScanCategorySecret}}
	if got := DiagnosticExcerpt(secrets, []byte(hostmarker.Rejected+" leaked")); got != diagnosticWithheldMarker {
		t.Fatalf("secret-bearing console output was disclosed: %q", got)
	}

	// The excerpt is bounded whatever the scanner writes.
	if got := DiagnosticExcerpt(sast, []byte(strings.Repeat("x", diagnosticExcerptBytes*4))); len(got) != diagnosticExcerptBytes {
		t.Fatalf("console excerpt ran to %d bytes, want %d", len(got), diagnosticExcerptBytes)
	}
	if got := DiagnosticSuffix("console", sast, []byte("   ")); got != "" {
		t.Fatalf("blank console output produced a suffix: %q", got)
	}

	// Console excerpts retain the tail, including literal marker text.
	forged := strings.Repeat("noise\n", 400) + hostmarker.Rejected + " stop scanning"
	excerpt := DiagnosticExcerpt(sast, []byte(forged))
	if !strings.Contains(excerpt, hostmarker.Rejected) {
		t.Fatal("console excerpt dropped marker text from the retained tail")
	}
}
