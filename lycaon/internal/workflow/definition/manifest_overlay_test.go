package definition

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectManifestOverlayWins(t *testing.T) {
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "plan")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	overlayYAML := "id: plan\nversion: 1.0.0\nname: project\ntrigger: /plan\nrequest:\n  question: What should we plan?\nphases:\n  - id: custom\n    activity_label: Custom phase\n"
	if err := os.WriteFile(filepath.Join(overlayDir, "workflow.yaml"), []byte(overlayYAML), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	registry, _, err := RegistryFromDirsWithSources(projectDir)
	testutil.FailErr(t, "load registry with project overlay", err)
	m := registry.All()[ManifestKey("plan", "1.0.0")]
	if m.Name != "project" {
		t.Fatalf("name = %q want project", m.Name)
	}
	if len(m.Phases) != 1 || m.Phases[0] != "custom" {
		t.Fatalf("phases = %v", m.Phases)
	}
}
