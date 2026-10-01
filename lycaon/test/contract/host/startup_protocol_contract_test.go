package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/lycaon/lycaon/internal/startupprotocol"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDesktopStartupProtocolPhasesStayInSync(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	rustRaw, err := os.ReadFile(filepath.Join(root, "lycaon-den/src-tauri/src/sidecar/startup.rs"))
	contractcheck.FailErr(t, "read Rust startup protocol", err)
	tsRaw, err := os.ReadFile(filepath.Join(root, "lycaon-den/src/platform/connection/engine-startup.ts"))
	contractcheck.FailErr(t, "read TypeScript startup protocol", err)

	rust := startupRustPhases(t, string(rustRaw))
	ts := startupTypeScriptPhases(t, string(tsRaw))
	goPhases := startupprotocol.Phases()
	want := make([]string, len(goPhases))
	for i, phase := range goPhases {
		want[i] = string(phase)
	}
	if strings.Join(rust, ",") != strings.Join(want, ",") {
		t.Errorf("Rust startup phases = %v, want %v", rust, want)
	}
	if len(want) == 0 || want[len(want)-1] != "ready" {
		t.Fatalf("terminal startup phase = %v, want ready", want)
	}
	visible := want[:len(want)-1]
	if strings.Join(ts, ",") != strings.Join(visible, ",") {
		t.Errorf("TypeScript startup phases = %v, want %v", ts, visible)
	}
}

func startupRustPhases(t *testing.T, source string) []string {
	t.Helper()
	match := regexp.MustCompile(`(?s)enum StartupPhase \{(.*?)\n\}`).FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatal("Rust StartupPhase enum not found")
	}
	variants := regexp.MustCompile(`(?m)^\s+([A-Z][A-Za-z]+),$`).FindAllStringSubmatch(match[1], -1)
	result := make([]string, 0, len(variants))
	for _, variant := range variants {
		result = append(result, snakeCase(variant[1]))
	}
	return result
}

func startupTypeScriptPhases(t *testing.T, source string) []string {
	t.Helper()
	match := regexp.MustCompile(`(?s)ENGINE_STARTUP_PHASES = \[(.*?)\] as const`).FindStringSubmatch(source)
	if len(match) != 2 {
		t.Fatal("TypeScript ENGINE_STARTUP_PHASES not found")
	}
	quoted := regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(match[1], -1)
	result := make([]string, 0, len(quoted))
	for _, item := range quoted {
		result = append(result, item[1])
	}
	return result
}

func snakeCase(value string) string {
	var out strings.Builder
	for i, r := range value {
		if i > 0 && unicode.IsUpper(r) {
			out.WriteByte('_')
		}
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}
