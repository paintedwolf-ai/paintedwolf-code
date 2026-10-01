package bundled_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

type releaseFixture struct {
	buildFixture
	raw     []byte
	archive string
}

func newReleaseFixture(t *testing.T) releaseFixture {
	t.Helper()
	f := releaseFixture{buildFixture: newBuildFixture(t)}
	entries, err := os.ReadDir(f.directory)
	testutil.FailErr(t, "read release fixture", err)
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(f.directory, entry.Name()))
		testutil.FailErr(t, "read release payload", err)
		testutil.FailErr(t, "release header", writer.WriteHeader(&tar.Header{Name: entry.Name(), Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(raw))}))
		_, err = writer.Write(raw)
		testutil.FailErr(t, "release payload", err)
	}
	testutil.FailErr(t, "close release tar", writer.Close())
	testutil.FailErr(t, "close release gzip", compressed.Close())
	f.pinArchive(t, buffer.Bytes())
	return f
}

func (f *releaseFixture) pinArchive(t *testing.T, raw []byte) {
	t.Helper()
	f.raw = raw
	f.archive = filepath.Join(t.TempDir(), "release.tar.gz")
	testutil.FailErr(t, "write release archive", os.WriteFile(f.archive, raw, 0o600))
	f.source.OpenGrep.Artifacts = []bundled.ReleaseArtifact{{GOOS: f.goos, GOARCH: f.goarch, URL: "https://example.invalid/opengrep.tar.gz", SHA256: buildDigest(raw), Bytes: int64(len(raw))}}
}

func (f releaseFixture) fetch(t *testing.T, options bundled.ReleaseFetchOptions) *bundled.ResolvedRelease {
	t.Helper()
	result, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, options)
	testutil.FailErr(t, "fetch qualified release", err)
	return result
}

func TestReleaseImportPortableOfflineCacheAndStaging(t *testing.T) {
	f := newReleaseFixture(t)
	root := t.TempDir()
	first := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: root, Archive: f.archive, Offline: true})
	second := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: root, Offline: true})
	if first.Directory != second.Directory {
		t.Fatal("valid cache did not reuse immutable generation")
	}
	relocated := filepath.Join(t.TempDir(), "relocated")
	testutil.FailErr(t, "move cache to another checkout", os.Rename(root, relocated))
	restored := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: relocated, Offline: true})
	relocated, err := filepath.EvalSymlinks(relocated)
	testutil.FailErr(t, "canonical relocated cache", err)
	if !strings.HasPrefix(restored.Directory, relocated+string(os.PathSeparator)) {
		t.Fatal("cache pointer was not portable")
	}
	staging := t.TempDir()
	binary, err := bundled.StageArtifact(t.Context(), restored.Manifest, staging, f.goos, f.goarch)
	testutil.FailErr(t, "stage released engine", err)
	if _, err := os.Stat(filepath.Join(filepath.Dir(binary), "NOTICES-opengrep.md")); err != nil {
		testutil.FailErr(t, "stage engine notices", err)
	}
	output := filepath.Join(t.TempDir(), "bundled-manifest.yaml")
	testutil.FailErr(t, "select admitted release", bundled.WriteReleaseManifest(t.Context(), restored.Manifest, output))
	raw, err := os.ReadFile(output)
	testutil.FailErr(t, "read selected manifest", err)
	if !bytes.Contains(raw, []byte(f.source.OpenGrep.Artifacts[0].SHA256)) {
		t.Fatal("selected YAML omitted outer archive identity")
	}
	if err := bundled.WriteReleaseManifest(t.Context(), f.source, output); err == nil {
		t.Fatal("unqualified descriptor wrote selected manifest")
	}
	preserved, err := os.ReadFile(output)
	testutil.FailErr(t, "read preserved selection", err)
	if !bytes.Equal(raw, preserved) {
		t.Fatal("failed selection changed previous manifest")
	}
}

func TestReleaseCacheRepairsPayloadWithoutTrustingEditedProvenance(t *testing.T) {
	f := newReleaseFixture(t)
	root := t.TempDir()
	first := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: root, Archive: f.archive})
	altered := []byte("different finalized executable")
	writeBuildFile(t, first.Directory, "opengrep", altered)
	f.provenance["binary_sha256"] = buildDigest(altered)
	f.provenance["binary_bytes"] = len(altered)
	writeBuildFile(t, first.Directory, "provenance.json", buildJSON(t, f.provenance))
	platformRaw, err := os.ReadFile(filepath.Join(first.Directory, "platform-checks.json"))
	testutil.FailErr(t, "read platform fixture", err)
	var platform map[string]any
	testutil.FailErr(t, "decode platform fixture", json.Unmarshal(platformRaw, &platform))
	platform["images"].([]any)[0].(map[string]any)["sha256"] = buildDigest(altered)
	rewritten := buildJSON(t, platform)
	writeBuildFile(t, first.Directory, "platform-checks.json", rewritten)
	f.provenance["platform_checks_sha256"] = buildDigest(rewritten)
	writeBuildFile(t, first.Directory, "provenance.json", buildJSON(t, f.provenance))
	if _, err := bundled.SelectBuildArtifact(f.source, first.Directory, f.goos, f.goarch); err != nil {
		testutil.FailErr(t, "self-consistent adversarial provenance", err)
	}
	repaired := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: root, Offline: true})
	if repaired.Directory == first.Directory {
		t.Fatal("edited local provenance bypassed the independent archive pin")
	}
	raw, err := os.ReadFile(filepath.Join(repaired.Directory, "opengrep"))
	testutil.FailErr(t, "read repaired executable", err)
	if bytes.Equal(raw, altered) {
		t.Fatal("tampered executable survived repair")
	}
}

func TestReleaseConcurrentFetchUsesOneVerifiedDownload(t *testing.T) {
	f := newReleaseFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = w.Write(f.raw) }))
	defer server.Close()
	f.source.OpenGrep.Artifacts[0].URL = server.URL + "/release.tar.gz"
	options := bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Client: server.Client()}
	const callers = 4
	results := make(chan *bundled.ResolvedRelease, callers)
	failures := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			result, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, options)
			results <- result
			failures <- err
		})
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		testutil.FailErr(t, "concurrent fetch", err)
	}
	var directory string
	for result := range results {
		if directory != "" && directory != result.Directory {
			t.Fatal("concurrent callers selected different generations")
		}
		directory = result.Directory
	}
	if requests.Load() != 1 {
		t.Fatalf("downloads = %d, want 1", requests.Load())
	}
}

func TestReleaseRejectsTransportCorruptionBeforePublication(t *testing.T) {
	for _, kind := range []string{"wrong bytes", "truncated", "oversized", "HTTP error"} {
		t.Run(kind, func(t *testing.T) {
			f := newReleaseFixture(t)
			root := t.TempDir()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				switch kind {
				case "wrong bytes":
					_, _ = w.Write(bytes.Repeat([]byte("x"), len(f.raw)))
				case "truncated":
					_, _ = w.Write(f.raw[:len(f.raw)-1])
				case "oversized":
					_, _ = w.Write(append(f.raw, 1))
				case "HTTP error":
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			f.source.OpenGrep.Artifacts[0].URL = server.URL
			_, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, bundled.ReleaseFetchOptions{CacheRoot: root, Client: server.Client()})
			if err == nil {
				t.Fatal("invalid download accepted")
			}
			matches, err := filepath.Glob(filepath.Join(root, "*.current"))
			testutil.FailErr(t, "inspect publication", err)
			if len(matches) != 0 {
				t.Fatal("failed download published a generation")
			}
		})
	}
}

func TestReleaseLockWaitIsCancelable(t *testing.T) {
	f := newReleaseFixture(t)
	root := t.TempDir()
	lock, err := filelock.Open(filepath.Join(root, f.source.OpenGrep.Artifacts[0].SHA256+".lock"))
	testutil.FailErr(t, "open held lock", err)
	defer func() { _ = lock.Close() }()
	locked, err := filelock.TryExclusive(lock)
	testutil.FailErr(t, "take held lock", err)
	if !locked {
		t.Fatal("fixture lock unavailable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = bundled.FetchReleaseArtifact(ctx, f.source, f.goos, f.goarch, bundled.ReleaseFetchOptions{CacheRoot: root, Archive: f.archive})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock acquisition: %v", err)
	}
}

func TestReleaseHTTPSRedirectCannotDowngrade(t *testing.T) {
	f := newReleaseFixture(t)
	var requests atomic.Int32
	insecure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); _, _ = w.Write(f.raw) }))
	defer insecure.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, insecure.URL, http.StatusFound) }))
	defer server.Close()
	f.source.OpenGrep.Artifacts[0].URL = server.URL
	if _, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Client: server.Client()}); err == nil {
		t.Fatal("HTTPS downgrade accepted")
	}
	if requests.Load() != 0 {
		t.Fatal("insecure redirect destination contacted")
	}
}

func TestReleaseRejectsTamperedArchiveAndExplicitBadImport(t *testing.T) {
	f := newReleaseFixture(t)
	root := t.TempDir()
	f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: root, Archive: f.archive})
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	testutil.FailErr(t, "write invalid import", os.WriteFile(bad, []byte("bad"), 0o600))
	if _, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, bundled.ReleaseFetchOptions{CacheRoot: root, Archive: bad}); err == nil {
		t.Fatal("invalid explicit import ignored because cache exists")
	}
	cached := filepath.Join(root, f.source.OpenGrep.Artifacts[0].SHA256+".tar.gz")
	testutil.FailErr(t, "tamper pinned archive", os.WriteFile(cached, []byte("bad"), 0o600))
	if _, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, bundled.ReleaseFetchOptions{CacheRoot: root, Offline: true}); err == nil {
		t.Fatal("cached generation bypassed corrupted outer pin")
	}
}

func TestReleaseBuildSelectionRechecksPinAcrossProcesses(t *testing.T) {
	f := newReleaseFixture(t)
	resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Archive: f.archive})
	_, err := bundled.SelectReleaseArtifact(t.Context(), f.source, resolved.Directory, f.goos, f.goarch)
	testutil.FailErr(t, "select cached release for build", err)
	writeBuildFile(t, resolved.Directory, "NOTICES-opengrep.md", []byte("changed notices"))
	if _, err := bundled.SelectReleaseArtifact(t.Context(), f.source, resolved.Directory, f.goos, f.goarch); err == nil {
		t.Fatal("changed cached release became build identity")
	}
	if _, err := bundled.SelectReleaseArtifact(t.Context(), f.source, f.directory, f.goos, f.goarch); err == nil {
		t.Fatal("unreleased source artifact became shipping identity")
	}
}

func TestReleaseManifestSelectionRejectsMutatedExportedPin(t *testing.T) {
	f := newReleaseFixture(t)
	resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: t.TempDir(), Archive: f.archive})
	resolved.Manifest.OpenGrep.Artifacts[0].SHA256 = buildDigest([]byte("other archive"))
	output := filepath.Join(t.TempDir(), "manifest.yaml")
	if err := bundled.WriteReleaseManifest(t.Context(), resolved.Manifest, output); err == nil {
		t.Fatal("changed pin bypassed admission")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unadmitted selection created manifest output")
	}
}
