package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteStartupDiagnosticsBuildsAStoreFreeRedactedArchive(t *testing.T) {
	configDir := t.TempDir()
	const secret = "cli-startup-provider-secret"
	for name, body := range map[string]string{
		"engine.log":           "starting\nOPENAI_API_KEY=" + secret + "\nfailed\n",
		"settings.yaml":        "OPENAI_API_KEY: " + secret + "\nposture: balanced\n",
		"credential-vault.age": "must-not-appear\n",
		// A malformed database verifies the store-free path.
		"store.db": "not a sqlite database",
	} {
		err := os.WriteFile(filepath.Join(configDir, name), []byte(body), 0o600)
		testutil.FailErr(t, "write startup fixture "+name, err)
	}

	var out bytes.Buffer
	err := writeStartupDiagnostics(
		t.Context(),
		[]string{"startup-diagnostics"},
		configDir,
		&out,
		time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
	)
	testutil.FailErr(t, "write startup diagnostics", err)
	if bytes.Contains(out.Bytes(), []byte(secret)) || bytes.Contains(out.Bytes(), []byte("must-not-appear")) {
		t.Fatal("startup CLI wrote an unredacted secret")
	}

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	testutil.FailErr(t, "open startup diagnostics zip", err)
	entries := make(map[string]struct{}, len(zr.File))
	for _, file := range zr.File {
		entries[file.Name] = struct{}{}
	}
	for _, want := range []string{
		"manifest.json",
		"health.json",
		"preflight.json",
		"config/settings.yaml",
		"logs/engine.log",
	} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("startup CLI archive missing %q: %v", want, entries)
		}
	}
	for _, forbidden := range []string{"config/credential-vault.age", "config/store.db", "logs/store.db"} {
		if _, ok := entries[forbidden]; ok {
			t.Fatalf("startup CLI archive included %q", forbidden)
		}
	}
}

func TestWriteStartupDiagnosticsRejectsAnyOtherInvocation(t *testing.T) {
	var out bytes.Buffer
	err := writeStartupDiagnostics(t.Context(), []string{"other"}, t.TempDir(), &out, time.Now())
	if err == nil || !strings.Contains(err.Error(), "usage: pw diagnostics startup-diagnostics") {
		t.Fatalf("unexpected diagnostics invocation error: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("invalid diagnostics invocation wrote %d bytes", out.Len())
	}
}

func TestServeRejectsUnsupportedStartupProtocolBeforeOpeningStore(t *testing.T) {
	t.Setenv("LYCAON_STARTUP_PROTOCOL", "unsupported")
	path := filepath.Join(t.TempDir(), "store.db")
	err := runServe(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "unsupported startup protocol version") {
		t.Fatalf("serve startup error = %v, want protocol refusal", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("protocol refusal touched store: %v", err)
	}
}
