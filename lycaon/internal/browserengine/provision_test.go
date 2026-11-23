package browserengine

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
)

// pinFixtureHash binds archive verification to the test fixture.
func pinFixtureHash(t *testing.T, plat string, zipBytes []byte) {
	t.Helper()
	prev := pinnedChromeHeadlessShellSHA256
	next := make(map[string]string, len(prev))
	for k, v := range prev {
		next[k] = v
	}
	sum := sha256.Sum256(zipBytes)
	next[plat] = hex.EncodeToString(sum[:])
	pinnedChromeHeadlessShellSHA256 = next
	t.Cleanup(func() { pinnedChromeHeadlessShellSHA256 = prev })
}

func TestEnsureManagedDownloads(t *testing.T) {
	t.Setenv(configlayout.EnvEngineRoot, "")
	plat, err := cftPlatform()
	if err != nil {
		t.Skip(err)
	}

	zipBytes := buildFakeHeadlessZip(t)
	pinFixtureHash(t, plat, zipBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantSuffix := "/" + PinnedChromeHeadlessShell + "/" + plat + "/chrome-headless-shell-" + plat + ".zip"
		if !strings.HasSuffix(r.URL.Path, wantSuffix) {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(zipBytes)
	}))
	t.Cleanup(srv.Close)

	prev := chromeForTestingBase
	chromeForTestingBase = srv.URL + "/chrome-for-testing-public"
	t.Cleanup(func() { chromeForTestingBase = prev })

	cache := t.TempDir()
	t.Setenv(EnvBrowserBin, "")
	t.Setenv(EnvBrowserSkipDownload, "0")

	path, err := ensureManaged(context.Background(), cache)
	testutil.FailErr(t, "ensureManaged failed", err)
	if !isExecutable(path) {
		t.Fatalf("not executable: %s", path)
	}
	resolved, err := EnsureBinary(context.Background(), ResolveOptions{CacheDir: cache})
	testutil.FailErr(t, "EnsureBinary failed", err)
	if resolved.Source != SourceManaged {
		t.Fatalf("source=%s", resolved.Source)
	}
}

// A transfer that fails mid-archive is retried from an empty file.
func TestEnsureManagedRetriesAFailedTransfer(t *testing.T) {
	plat, err := cftPlatform()
	if err != nil {
		t.Skip(err)
	}

	zipBytes := buildFakeHeadlessZip(t)
	pinFixtureHash(t, plat, zipBytes)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(zipBytes)))
			_, _ = w.Write(zipBytes[:len(zipBytes)/2])
			return
		}
		_, _ = w.Write(zipBytes)
	}))
	t.Cleanup(srv.Close)
	prev := chromeForTestingBase
	chromeForTestingBase = srv.URL + "/chrome-for-testing-public"
	t.Cleanup(func() { chromeForTestingBase = prev })
	t.Setenv(EnvBrowserBin, "")
	t.Setenv(EnvBrowserSkipDownload, "0")

	path, err := ensureManaged(context.Background(), t.TempDir())
	testutil.FailErr(t, "ensureManaged failed", err)
	if !isExecutable(path) || requests.Load() != 2 {
		t.Fatalf("provisioned %s after %d requests, want an executable after 2", path, requests.Load())
	}
}

// A provision whose every attempt fails is refused with its cause, after the bounded retries.
func TestEnsureBinaryRefusesAProvisionThatFailsEveryAttempt(t *testing.T) {
	if _, err := cftPlatform(); err != nil {
		t.Skip(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	prev := chromeForTestingBase
	chromeForTestingBase = srv.URL + "/chrome-for-testing-public"
	t.Cleanup(func() { chromeForTestingBase = prev })
	t.Setenv(EnvBrowserBin, "")
	t.Setenv(EnvBrowserSkipDownload, "0")
	cache := t.TempDir()

	_, err := ensureManaged(context.Background(), cache)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("attempt %d: download", downloadAttempts)) ||
		requests.Load() != downloadAttempts {
		t.Fatalf("ensureManaged = %v after %d requests, want every attempt's failure after %d", err, requests.Load(), downloadAttempts)
	}
	_, err = EnsureBinary(context.Background(), ResolveOptions{CacheDir: cache, AllowDownload: true})
	var reject *RejectError
	if !errors.As(err, &reject) || reject.Code != "BROWSER_UNAVAILABLE" || reject.Data["reason"] != "provision_failed" {
		t.Fatalf("EnsureBinary = %v, want BROWSER_UNAVAILABLE provision_failed", err)
	}
}

// A cancelled caller is not retried.
func TestDownloadArchiveStopsRetryingOnceTheCallerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	file, err := os.CreateTemp(t.TempDir(), "archive-*.zip")
	testutil.FailErr(t, "create archive file", err)
	t.Cleanup(func() { _ = file.Close() })

	if err := downloadArchive(ctx, srv.URL, file); err == nil || requests.Load() != 1 {
		t.Fatalf("downloadArchive = %v after %d requests, want one failed attempt", err, requests.Load())
	}
}

// An archive file that cannot be emptied is refused before any request.
func TestDownloadArchiveRefusesAFileItCannotRewind(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { requests.Add(1) }))
	t.Cleanup(srv.Close)
	file, err := os.CreateTemp(t.TempDir(), "archive-*.zip")
	testutil.FailErr(t, "create archive file", err)
	testutil.FailErr(t, "close archive file", file.Close())

	if err := downloadArchive(context.Background(), srv.URL, file); !errors.Is(err, os.ErrClosed) || requests.Load() != 0 {
		t.Fatalf("downloadArchive = %v after %d requests, want os.ErrClosed before any request", err, requests.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// An unresolvable host is reported as such after one attempt.
func TestDownloadArchiveReportsAnUnresolvableHostOnce(t *testing.T) {
	var requests atomic.Int32
	prev := downloadClient
	downloadClient = func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, &net.DNSError{Err: "read udp 127.0.0.1:41991->127.0.0.53:53: read: connection refused",
				Name: "storage.googleapis.com", Server: "127.0.0.53:53"}
		})}
	}
	t.Cleanup(func() { downloadClient = prev })
	file, err := os.CreateTemp(t.TempDir(), "archive-*.zip")
	testutil.FailErr(t, "create archive file", err)
	t.Cleanup(func() { _ = file.Close() })

	err = downloadArchive(context.Background(), "https://storage.googleapis.com/archive.zip", file)
	var unresolved *net.DNSError
	if !errors.As(err, &unresolved) || !strings.Contains(err.Error(), "cannot resolve storage.googleapis.com") ||
		requests.Load() != 1 {
		t.Fatalf("downloadArchive = %v after %d requests, want one unresolvable-host failure", err, requests.Load())
	}
}

// The managed cache retains only the pinned browser version.
func TestEnsureManagedPrunesSupersededVersions(t *testing.T) {
	plat, err := cftPlatform()
	if err != nil {
		t.Skip(err)
	}

	zipBytes := buildFakeHeadlessZip(t)
	pinFixtureHash(t, plat, zipBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	t.Cleanup(srv.Close)
	prev := chromeForTestingBase
	chromeForTestingBase = srv.URL + "/chrome-for-testing-public"
	t.Cleanup(func() { chromeForTestingBase = prev })

	cache := t.TempDir()
	superseded := filepath.Join(cache, "managed", "100.0.0.0", plat)
	testutil.FailErr(t, "mkdir superseded", os.MkdirAll(superseded, 0o755))
	testutil.FailErr(t, "write superseded", os.WriteFile(filepath.Join(superseded, ".complete"), []byte("100.0.0.0\n"), 0o644))
	t.Setenv(EnvBrowserBin, "")
	t.Setenv(EnvBrowserSkipDownload, "0")

	path, err := ensureManaged(context.Background(), cache)
	testutil.FailErr(t, "ensureManaged failed", err)
	if !isExecutable(path) {
		t.Fatalf("not executable: %s", path)
	}
	if _, err := os.Stat(superseded); !os.IsNotExist(err) {
		t.Fatalf("superseded version survived provisioning: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "managed", PinnedChromeHeadlessShell, plat, ".complete")); err != nil {
		t.Fatalf("pinned version missing after prune: %v", err)
	}
}

func TestChromeForTestingPlatformMatrixIncludesReleaseTargets(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"darwin/arm64": "mac-arm64",
		"linux/amd64":  "linux64",
		"linux/arm64":  "linux-arm64",
	} {
		parts := strings.Split(input, "/")
		got, err := cftPlatformFor(parts[0], parts[1])
		testutil.FailErr(t, "map release platform to Chrome for Testing", err)
		if got != want {
			t.Fatalf("cftPlatformFor(%q) = %q want %q", input, got, want)
		}
		if pinnedChromeHeadlessShellSHA256[got] == "" {
			t.Fatalf("release platform %q has no pinned browser archive", input)
		}
	}
}

// The managed browser tree is included in the signed application bundle.
func TestEnsureManagedRejectsHashMismatch(t *testing.T) {
	plat, err := cftPlatform()
	if err != nil {
		t.Skip(err)
	}

	zipBytes := buildFakeHeadlessZip(t)
	prev := pinnedChromeHeadlessShellSHA256
	next := make(map[string]string, len(prev))
	for k, v := range prev {
		next[k] = v
	}
	wrong := strings.Repeat("ab", 32)
	next[plat] = wrong
	pinnedChromeHeadlessShellSHA256 = next
	t.Cleanup(func() { pinnedChromeHeadlessShellSHA256 = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	t.Cleanup(srv.Close)

	prevBase := chromeForTestingBase
	chromeForTestingBase = srv.URL + "/chrome-for-testing-public"
	t.Cleanup(func() { chromeForTestingBase = prevBase })

	cache := t.TempDir()
	_, err = ensureManaged(context.Background(), cache)
	if err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") || !strings.Contains(err.Error(), wrong) {
		t.Fatalf("error = %v, want mismatch naming expected hash", err)
	}
	if ManagedBinary(cache) != "" {
		t.Fatal("unverified archive was installed")
	}
}

func TestEnsureManagedRejectsMissingPin(t *testing.T) {
	plat, err := cftPlatform()
	if err != nil {
		t.Skip(err)
	}

	zipBytes := buildFakeHeadlessZip(t)
	prev := pinnedChromeHeadlessShellSHA256
	next := make(map[string]string, len(prev))
	for k, v := range prev {
		next[k] = v
	}
	delete(next, plat)
	pinnedChromeHeadlessShellSHA256 = next
	t.Cleanup(func() { pinnedChromeHeadlessShellSHA256 = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	t.Cleanup(srv.Close)

	prevBase := chromeForTestingBase
	chromeForTestingBase = srv.URL + "/chrome-for-testing-public"
	t.Cleanup(func() { chromeForTestingBase = prevBase })

	cache := t.TempDir()
	_, err = ensureManaged(context.Background(), cache)
	if err == nil {
		t.Fatal("expected missing-pin error")
	}
	if !strings.Contains(err.Error(), "no pinned sha256") {
		t.Fatalf("error = %v, want missing-pin refusal", err)
	}
	if ManagedBinary(cache) != "" {
		t.Fatal("unverified archive was installed")
	}
}

func TestPinnedHashCoversAllPlatforms(t *testing.T) {
	for _, plat := range []string{"mac-arm64", "mac-x64", "linux64", "linux-arm64", "win64", "win32"} {
		want, ok := pinnedChromeHeadlessShellSHA256[plat]
		if !ok {
			t.Fatalf("no sha256 pin for %s", plat)
		}
		if len(want) != 64 {
			t.Fatalf("pin for %s is not a sha256 hex digest: %q", plat, want)
		}
		if _, err := hex.DecodeString(want); err != nil {
			t.Fatalf("pin for %s is not hex: %v", plat, err)
		}
	}
}

func buildFakeHeadlessZip(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "out.zip")
	f, err := os.Create(zipPath)
	testutil.FailErr(t, "os.Create failed", err)
	zw := zip.NewWriter(f)
	name := "chrome-headless-shell-fake/" + headlessShellName()
	w, err := zw.Create(name)
	testutil.FailErr(t, "zw.Create failed", err)
	if _, err := w.Write([]byte("#!/bin/sh\necho fake\n")); err != nil {
		testutil.FailErr(t, "w.Write failed", err)
	}
	icu, err := zw.Create("chrome-headless-shell-fake/icudtl.dat")
	testutil.FailErr(t, "zw.Create failed", err)
	if _, err := icu.Write([]byte("icu")); err != nil {
		testutil.FailErr(t, "icu.Write failed", err)
	}
	if err := zw.Close(); err != nil {
		testutil.FailErr(t, "zw.Close failed", err)
	}
	if err := f.Close(); err != nil {
		testutil.FailErr(t, "f.Close failed", err)
	}
	b, err := os.ReadFile(zipPath)
	testutil.FailErr(t, "read file", err)
	return b
}

// Production browser execution requires the staged engine tree.
func TestEnsureBinaryProductionNeverDownloads(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	prev := chromeForTestingBase
	chromeForTestingBase = srv.URL + "/chrome-for-testing-public"
	t.Cleanup(func() { chromeForTestingBase = prev })

	t.Setenv(EnvBrowserBin, "")
	t.Setenv(EnvBrowserSkipDownload, "0")

	// Production wiring leaves AllowDownload false.
	resolved, err := EnsureBinary(context.Background(), ResolveOptions{CacheDir: t.TempDir()})

	if requests != 0 {
		t.Fatalf("production resolve made %d network requests, want 0", requests)
	}
	if err == nil && resolved.Source == SourceManaged {
		t.Fatal("production resolve returned a managed browser: something was provisioned without AllowDownload")
	}
	// Tool errors and readiness cards share the unavailable code.
	if err != nil && !strings.Contains(err.Error(), "BROWSER_ENGINE_UNAVAILABLE") {
		t.Fatalf("error = %v, want BROWSER_ENGINE_UNAVAILABLE", err)
	}
}

// An empty cache is a typed refusal even on a machine with a browser installed.
func TestEnsureBinaryProductionTypedErrorWhenNothingResolves(t *testing.T) {
	t.Setenv(configlayout.EnvEngineRoot, "")
	t.Setenv(EnvBrowserBin, "")
	t.Setenv("PATH", t.TempDir())

	_, err := EnsureBinary(context.Background(), ResolveOptions{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected a typed failure when no hermetic browser is present")
	}
	if !strings.Contains(err.Error(), "BROWSER_ENGINE_UNAVAILABLE") {
		t.Fatalf("error = %v, want BROWSER_ENGINE_UNAVAILABLE", err)
	}
}

// App-bundle launchers lack the sibling resources required by installReady.
func TestResolveBinaryRejectsAnAppBundleLauncherStub(t *testing.T) {
	t.Setenv(configlayout.EnvEngineRoot, "")
	stub := filepath.Join(t.TempDir(), "Google Chrome.app", "Contents", "MacOS")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	bin := filepath.Join(stub, "Google Chrome")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	frameworks := filepath.Join(filepath.Dir(stub), "Frameworks", "Resources")
	if err := os.MkdirAll(frameworks, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(frameworks, "icudtl.dat"), []byte("icu"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	t.Setenv(EnvBrowserBin, bin)
	if got, ok := ResolveBinary(ResolveOptions{CacheDir: t.TempDir()}); ok {
		t.Fatalf("resolved an app-bundle browser: %+v", got)
	}
	_, err := EnsureBinary(context.Background(), ResolveOptions{CacheDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "BROWSER_ENGINE_UNAVAILABLE") {
		t.Fatalf("error = %v, want BROWSER_ENGINE_UNAVAILABLE", err)
	}
}

// The explicit cache directory also defines the confinement write root.
func TestManagedRootRequiresAnExplicitCacheDir(t *testing.T) {
	if root := managedRoot(""); root != "" {
		t.Fatalf("managedRoot(\"\") = %q, want no managed tree", root)
	}
	if ManagedBinary("") != "" {
		t.Fatal("ManagedBinary(\"\") resolved a binary without a named cache")
	}
}
