package logscli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/logview"
)

func TestEditConfigRunsConfiguredEditor(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	binDir := t.TempDir()
	argLog := filepath.Join(binDir, "editor-arg.txt")
	editorPath := filepath.Join(binDir, "record-editor.sh")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > " + argLog + "\n"
	if err := os.WriteFile(editorPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write editor fixture: %v", err)
	}
	t.Setenv("EDITOR", editorPath)

	if err := editConfig(logview.DefaultConfig()); err != nil {
		t.Fatalf("editConfig with fixture editor: %v", err)
	}

	wantPath := filepath.Join(cfgDir, "logview.yaml")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("stat saved logview.yaml: %v", err)
	}
	gotArg, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatalf("read recorded editor argument: %v", err)
	}
	if got := strings.TrimSpace(string(gotArg)); got != wantPath {
		t.Fatalf("editor received path %q, want %q", got, wantPath)
	}
}
