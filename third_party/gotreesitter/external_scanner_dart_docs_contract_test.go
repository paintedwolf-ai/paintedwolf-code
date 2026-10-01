package gotreesitter

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

var dartScannerMatrixRowRE = regexp.MustCompile("(?m)^\\| `dart` \\| ([^|]+) \\|$")

func TestDartExternalScannerDocumentationBoundMatchesRuntime(t *testing.T) {
	content, err := os.ReadFile("docs/external-scanners.md")
	if err != nil {
		t.Fatal(err)
	}
	match := dartScannerMatrixRowRE.FindStringSubmatch(string(content))
	if len(match) != 2 {
		t.Fatal("external-scanner matrix is missing the Dart row")
	}
	if dartIncrementalReuseMaxSourceBytes%1024 != 0 {
		t.Fatalf("Dart incremental-reuse bound %d is not an exact KiB value", dartIncrementalReuseMaxSourceBytes)
	}
	want := fmt.Sprintf("bounded reuse (up to %d KiB)", dartIncrementalReuseMaxSourceBytes/1024)
	if got := strings.TrimSpace(match[1]); got != want {
		t.Fatalf("Dart scanner matrix contract = %q, want %q", got, want)
	}
}
