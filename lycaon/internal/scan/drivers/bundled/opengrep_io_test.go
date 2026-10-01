package bundleddriver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOpengrepTempJSONPathUsesPrivateDir(t *testing.T) {
	files, cleanup, err := newOpengrepRunFiles()
	testutil.FailErr(t, "newOpengrepRunFiles", err)
	defer cleanup()

	if !strings.HasPrefix(files.runDir, filepath.Clean(os.TempDir())+string(filepath.Separator)) {
		t.Fatalf("run dir = %q, want under OS temp %q", files.runDir, os.TempDir())
	}
	st, err := os.Stat(files.jsonPath)
	testutil.FailErr(t, "stat temp json", err)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", st.Mode().Perm())
	}
	dirStat, err := os.Stat(files.runDir)
	testutil.FailErr(t, "stat run dir", err)
	if dirStat.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 0700", dirStat.Mode().Perm())
	}
}

func TestOpenOpenGrepJSONOutputRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, []byte(`{"results":[]}`), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink not supported")
	}
	if _, err := openOpenGrepJSONOutput(link); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestOpenGrepJSONOutputHasNoReportSizeCeiling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	testutil.FailErr(t, "create large report", err)
	if _, err := f.WriteString(`{"ignored":[`); err != nil {
		testutil.FailErr(t, "write report prefix", err)
	}
	chunk := `"` + strings.Repeat("x", 32*1024) + `",`
	for range 33 * 32 {
		if _, err := f.WriteString(chunk); err != nil {
			testutil.FailErr(t, "write large report", err)
		}
	}
	if _, err := f.WriteString(`null],"results":[],"errors":[]}`); err != nil {
		testutil.FailErr(t, "write report suffix", err)
	}
	testutil.FailErr(t, "close large report", f.Close())

	report, err := openOpenGrepJSONOutput(path)
	testutil.FailErr(t, "openOpenGrepJSONOutput", err)
	defer func() { _ = report.Close() }()
	result, err := scanoutput.ParseOpengrepJSON(report)
	testutil.FailErr(t, "parse large report", err)
	if result.FindingsCount != 0 {
		t.Fatalf("findings = %d, want 0", result.FindingsCount)
	}
	if info, err := os.Stat(path); err != nil || info.Size() <= 32<<20 {
		t.Fatalf("report size = %d, err = %v", info.Size(), err)
	}
}

func TestOpenOpenGrepJSONOutputOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	payload := `{"results":[],"errors":[]}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	report, err := openOpenGrepJSONOutput(path)
	testutil.FailErr(t, "openOpenGrepJSONOutput", err)
	defer func() { _ = report.Close() }()
	raw := make([]byte, len(payload))
	if _, err := report.Read(raw); err != nil {
		testutil.FailErr(t, "read report", err)
	}
	if string(raw) != payload {
		t.Fatalf("payload = %q", raw)
	}
}

func TestOpengrepTempCleanupRemovesFile(t *testing.T) {
	files, cleanup, err := newOpengrepRunFiles()
	testutil.FailErr(t, "newOpengrepRunFiles", err)
	for _, path := range []string{files.logPath, files.settingsPath, files.versionCachePath} {
		if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
			testutil.FailErr(t, "write subprocess state", err)
		}
	}
	cleanup()
	if _, err := os.Stat(files.runDir); !os.IsNotExist(err) {
		t.Fatalf("expected run directory removed, stat err = %v", err)
	}
	for _, path := range []string{
		files.jsonPath,
		files.logPath,
		files.settingsPath,
		files.versionCachePath,
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected temp %q removed, stat err = %v", path, err)
		}
	}
}

func TestOpengrepSubprocessEnvKeepsCLIStateInRunFiles(t *testing.T) {
	files, cleanup, err := newOpengrepRunFiles()
	testutil.FailErr(t, "newOpengrepRunFiles", err)
	defer cleanup()

	got := make(map[string]string)
	for _, assignment := range opengrepSubprocessEnv(files) {
		key, value, ok := strings.Cut(assignment, "=")
		if ok {
			got[key] = value
		}
	}
	want := map[string]string{
		"SEMGREP_LOG_FILE":           files.logPath,
		"SEMGREP_SETTINGS_FILE":      files.settingsPath,
		"SEMGREP_VERSION_CACHE_PATH": files.versionCachePath,
		"SEMGREP_SEND_METRICS":       "off",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
}
