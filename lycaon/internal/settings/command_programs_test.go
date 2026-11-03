package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandProgramsSettingsSelectExternalVisibility(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "command-programs.yaml")
	got, err := LoadCommandProgramsSettingsAt(path)
	testutil.FailErr(t, "load default", err)
	if got.ExternalHidden() {
		t.Fatal("external hidden without the setting")
	}
	testutil.FailErr(t, "write hidden", os.WriteFile(path, []byte("external_selector: hidden\n"), 0o600))
	got, err = LoadCommandProgramsSettingsAt(path)
	testutil.FailErr(t, "load hidden", err)
	if !got.ExternalHidden() {
		t.Fatal("hidden setting not applied")
	}
	testutil.FailErr(t, "write invalid", os.WriteFile(path, []byte("external_selector: maybe\n"), 0o600))
	if _, err := LoadCommandProgramsSettingsAt(path); err == nil {
		t.Fatal("unknown selector accepted")
	}
}
