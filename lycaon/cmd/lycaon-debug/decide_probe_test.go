package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/testutil"
)

// probeEngine answers the engine protocol from a shell script: hello, then a
// turn-load decision that loads web_search and task.
const probeEngine = `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"hello"'*) printf '{"id":%s,"engine":{"name":"fake-bialy","model":"test","device":"vulkan:0","adapter":"Fake GPU","probe_ms":265.5,"head":"turn-load"}}\n' "$id" ;;
    *'"method":"decide"'*) printf '{"id":%s,"answers":{"tools":{"type":"multi","probabilities":{"web_search":0.9,"task":0.8},"confidence":0.9}}}\n' "$id" ;;
    *) printf '{"id":%s,"error":"unknown method"}\n' "$id" ;;
  esac
done
`

func writeProbeEngine(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake engine")
	}
	path := filepath.Join(t.TempDir(), "bialy")
	testutil.FailErr(t, "write fake engine", os.WriteFile(path, []byte(probeEngine), 0o755))
	return path
}

// writeProbeModel lays out the shipped checkpoint files the client requires.
func writeProbeModel(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range bialy.ShippedModel.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write model file", os.WriteFile(path, []byte("x"), 0o644))
	}
	testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(dir, bialy.CompleteMarker), []byte("test\n"), 0o644))
	return dir
}

func TestDecideProbeAnswersThroughTheEngine(t *testing.T) {
	t.Setenv("LYCAON_DECIDE_BINARY", writeProbeEngine(t))
	t.Setenv("LYCAON_DECIDE_MODEL_DIR", writeProbeModel(t))
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())

	out, err := captureStdout(t, func() error {
		return runDecideProbe([]string{"--request", "search the web for the changelog format"})
	})
	testutil.FailErr(t, "decide probe", err)
	var report struct {
		Engine   decide.Engine     `json:"engine"`
		State    turnload.State    `json:"state"`
		Decision turnload.Decision `json:"decision"`
	}
	testutil.FailErr(t, "decode probe report", json.Unmarshal([]byte(out), &report))
	if report.Engine.Name != "fake-bialy" {
		t.Fatalf("engine = %+v, want the fake engine identity", report.Engine)
	}
	if report.State.User != "search the web for the changelog format" || report.State.Surface != "implement_investigate" {
		t.Fatalf("state = %+v", report.State)
	}
	if report.Decision.Abstained || report.Decision.Failure != "" {
		t.Fatalf("decision = %+v, want an answered decision", report.Decision)
	}
	if len(report.Decision.Tools) != 2 || report.Decision.Tools["web_search"] != 0.9 || report.Decision.Tools["task"] != 0.8 {
		t.Fatalf("tools = %+v, want web_search and task loaded", report.Decision.Tools)
	}
	if strings.Contains(out, `"candidates"`) {
		t.Fatal("probe echoed the scored candidate set")
	}
}

func TestDecideProbeRejectsBadInput(t *testing.T) {
	if err := runDecideProbe(nil); err == nil || !strings.Contains(err.Error(), "--request is required") {
		t.Fatalf("missing --request error = %v", err)
	}
	if err := runDecideProbe([]string{"--request", "fix the typo", "--surface", "elsewhere"}); err == nil || !strings.Contains(err.Error(), "unknown surface") {
		t.Fatalf("unknown surface error = %v", err)
	}
}
