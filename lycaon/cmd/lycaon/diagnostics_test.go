package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hostpower"
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

func TestDescribeSystemProxyReportsTheHostDecision(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(key, "")
	}
	t.Setenv("HTTPS_PROXY", "http://alice:secret@proxy.example:3128")
	var out bytes.Buffer
	testutil.FailErr(t, "describe proxied target", describeSystemProxy("https://api.example/v1", &out))
	if out.String() != "http://redacted@proxy.example:3128\n" {
		t.Fatalf("proxied target = %q", out.String())
	}
	out.Reset()
	testutil.FailErr(t, "describe loopback", describeSystemProxy("http://127.0.0.1:8787/health", &out))
	if out.String() != "direct\n" {
		t.Fatalf("loopback = %q", out.String())
	}
	for _, raw := range []string{"", "api.example", "://x", "https://"} {
		if err := describeSystemProxy(raw, &out); err == nil {
			t.Fatalf("%q was accepted as a target", raw)
		}
	}
}

func TestHoldPowerInhibitorReportsProviderOrUnavailability(t *testing.T) {
	var out bytes.Buffer
	err := holdPowerInhibitor(t.Context(), hostpower.New(true), 0, &out)
	line := out.String()
	if !strings.HasPrefix(line, "supported=") || !strings.Contains(line, "provider=") || !strings.Contains(line, "unavailable=") {
		t.Fatalf("status line = %q", line)
	}
	if strings.Contains(line, "inhibiting=true") != (err == nil) {
		t.Fatalf("status %q with error %v", line, err)
	}
	if err != nil && strings.HasSuffix(strings.TrimSpace(err.Error()), ":") {
		t.Fatalf("unavailable inhibition gave no reason: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	out.Reset()
	if err := holdPowerInhibitor(canceled, hostpower.New(true), time.Hour, &out); err == nil || (!strings.Contains(out.String(), "inhibiting=false") && !errors.Is(err, context.Canceled)) {
		t.Fatalf("canceled hold = %v with %q", err, out.String())
	}
}

func TestProbeGitRemoteRunsThroughTheBundledToolchain(t *testing.T) {
	var out bytes.Buffer
	if err := probeGitRemote(t.Context(), "ext::sh -c touch%20/tmp/pwned", &out); err == nil {
		t.Fatal("remote-helper transport was accepted")
	}
	// The bundled git resolves once per process, so one branch runs per test binary.
	engineRoot := filepath.Join(testutil.CheckoutRoot(t), "lycaon-den", "src-tauri", "engine-root")
	if _, err := os.Stat(filepath.Join(engineRoot, "gitengine", "bin", "git")); err != nil {
		t.Setenv("LYCAON_ENGINE_ROOT", t.TempDir())
		if err := probeGitRemote(t.Context(), "https://example.com/repo.git", &out); err == nil {
			t.Fatal("missing bundled git did not fail the probe")
		}
		return
	}
	t.Setenv("LYCAON_ENGINE_ROOT", engineRoot)
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	remote := filepath.Join(t.TempDir(), "remote.git")
	if output, err := exec.Command(filepath.Join(engineRoot, "gitengine", "bin", "git"), "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare remote: %v: %s", err, output)
	}
	testutil.FailErr(t, "probe local remote", probeGitRemote(t.Context(), "file://"+remote, &out))
	if out.String() != "refs=0\n" {
		t.Fatalf("empty remote probe = %q", out.String())
	}
	if err := probeGitRemote(t.Context(), "file://"+filepath.Join(t.TempDir(), "absent.git"), &out); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("absent remote = %v", err)
	}
}
