package external

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const sarifFixture = `{"version":"2.1.0","$schema":"https://json.schemastore.org/sarif-2.1.0.json","runs":[{"tool":{"driver":{"name":"fake"}},"results":[{"ruleId":"FAKE001","level":"error","message":{"text":"leaked key"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.txt"},"region":{"startLine":1}}}]}]}]}`

func newEntry(t *testing.T, command []string) scancatalog.ScannerEntry {
	t.Helper()
	off := true
	return scancatalog.ScannerEntry{
		ID:           "fake-ext",
		Driver:       scancatalog.DriverExternal,
		Categories:   []string{"secret"},
		Command:      command,
		OutputParser: scanoutput.OutputParserSARIF,
		Enabled:      &off,
	}
}

func TestExternalScannerRunFilesStayInOnePrivateDirectory(t *testing.T) {
	entry := newEntry(t, []string{"scanner", scancatalog.ArgTokenReportPath})
	scanner, err := NewScanner(entry, t.TempDir(), "", "")
	testutil.FailErr(t, "new scanner", err)

	dir, reportPath, cleanup, err := scanner.newRunFiles()
	testutil.FailErr(t, "new run files", err)
	defer cleanup()
	if filepath.Dir(reportPath) != dir {
		t.Fatalf("report path = %q, want direct child of %q", reportPath, dir)
	}
	dirInfo, err := os.Stat(dir)
	testutil.FailErr(t, "stat run directory", err)
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("run directory mode = %o, want 0700", dirInfo.Mode().Perm())
	}
	reportInfo, err := os.Stat(reportPath)
	testutil.FailErr(t, "stat report file", err)
	if reportInfo.Mode().Perm() != 0o600 {
		t.Fatalf("report mode = %o, want 0600", reportInfo.Mode().Perm())
	}

	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("run directory remained after cleanup: %v", err)
	}
}

// Report-file scanners write to a host-managed path.
func TestExternalScannerReadsReportFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh to emulate a file-reporting scanner")
	}
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "a.txt"), []byte("x"), 0o600); err != nil {
		testutil.FailErr(t, "seed project file", err)
	}

	// Stdout noise does not replace the report body.
	entry := newEntry(t, []string{
		"sh", "-c",
		`printf '%s' '` + sarifFixture + `' > "$1"; echo "scanning complete"; exit 1`,
		"sh", scancatalog.ArgTokenReportPath,
	})

	scanner, err := NewScanner(entry, projectDir, "", "")
	if err != nil {
		testutil.FailErr(t, "new scanner", err)
	}
	result, err := scanner.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
	if err != nil {
		testutil.FailErr(t, "run file-reporting scanner", err)
	}
	if result.FindingsCount != 1 {
		t.Fatalf("findings = %d, want 1", result.FindingsCount)
	}
	props := result.Findings[0].Properties
	if props == nil || props.Lycaon == nil || props.Lycaon.Kind != api.FindingKindSecret ||
		len(props.Lycaon.Categories) != 1 || props.Lycaon.Categories[0] != api.ScanCategorySecret {
		t.Fatalf("external SARIF classification = %#v", props)
	}
}

// A stdout-reporting scanner parses cleanly even when it also writes to stderr.
func TestExternalScannerIgnoresStderrNoise(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh to emulate a stdout-reporting scanner")
	}
	projectDir := t.TempDir()
	entry := newEntry(t, []string{
		"sh", "-c",
		`echo "INFO loading rules" 1>&2; printf '%s' '` + sarifFixture + `'; exit 1`,
	})

	scanner, err := NewScanner(entry, projectDir, "", "")
	if err != nil {
		testutil.FailErr(t, "new scanner", err)
	}
	result, err := scanner.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
	if err != nil {
		testutil.FailErr(t, "run stdout scanner with stderr noise", err)
	}
	if result.FindingsCount != 1 {
		t.Fatalf("findings = %d, want 1", result.FindingsCount)
	}
}

// A missing report includes bounded stderr context.
func TestExternalScannerReportFileMissingSurfacesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh to emulate a failing scanner")
	}
	projectDir := t.TempDir()
	entry := newEntry(t, []string{
		"sh", "-c",
		`rm -f "$1"; echo "fatal: bad config" 1>&2; exit 2`,
		"sh", scancatalog.ArgTokenReportPath,
	})
	entry.Categories = []string{"sast"}

	scanner, err := NewScanner(entry, projectDir, "", "")
	if err != nil {
		testutil.FailErr(t, "new scanner", err)
	}
	if _, err = scanner.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir}); err == nil {
		t.Fatal("expected an error when the scanner writes no report")
	}
	if got := err.Error(); !strings.Contains(got, "fatal: bad config") {
		t.Fatalf("error = %q, want the tool's stderr", got)
	}
}

func TestReadReportFileEnforcesMaterializationLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		testutil.FailErr(t, "write report", err)
	}
	if _, err := readReportFile(path, 8); err == nil || !strings.Contains(err.Error(), "8 byte limit") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadReportFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		testutil.FailErr(t, "write target", err)
	}
	link := filepath.Join(dir, "report.json")
	if err := os.Symlink(target, link); err != nil {
		testutil.FailErr(t, "create symlink", err)
	}
	if _, err := readReportFile(link, 8); err == nil {
		t.Fatal("expected symlink report rejection")
	}
}

func TestScannerEnvironmentKeepsBaselineAndExplicitNames(t *testing.T) {
	base := []string{
		"PATH=/usr/bin", "HOME=/home/test", "LANG=en_US.UTF-8", "LC_CTYPE=UTF-8",
		"SCANNER_TOKEN=secret", "UNRELATED_SECRET=hidden",
	}
	got := scannerEnvironment(base, []string{"SCANNER_TOKEN"})
	joined := strings.Join(got, "\n")
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/test", "LANG=en_US.UTF-8", "LC_CTYPE=UTF-8", "SCANNER_TOKEN=secret"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment missing %q: %v", want, got)
		}
	}
	if strings.Contains(joined, "UNRELATED_SECRET") {
		t.Fatalf("unrelated variable leaked: %v", got)
	}
}
