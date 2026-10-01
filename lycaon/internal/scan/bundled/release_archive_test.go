package bundled_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func alterReleaseTar(t *testing.T, raw []byte, kind string) []byte {
	t.Helper()
	compressed, err := gzip.NewReader(bytes.NewReader(raw))
	testutil.FailErr(t, "open release compression", err)
	decompressed, err := io.ReadAll(compressed)
	testutil.FailErr(t, "decompress release", err)
	testutil.FailErr(t, "close compressed fixture", compressed.Close())
	switch kind {
	case "missing tar end":
		decompressed = decompressed[:len(decompressed)-1024]
	case "trailing tar payload":
		decompressed = append(decompressed, []byte("hidden payload")...)
	case "unaligned tar padding":
		decompressed = append(decompressed, 0)
	default:
		decompressed = rewriteReleaseTar(t, decompressed, kind)
	}
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	_, err = writer.Write(decompressed)
	testutil.FailErr(t, "compress changed archive", err)
	testutil.FailErr(t, "close changed compression", writer.Close())
	return output.Bytes()
}

func rewriteReleaseTar(t *testing.T, raw []byte, kind string) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(raw))
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	index := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		testutil.FailErr(t, "read original header", err)
		payload, err := io.ReadAll(reader)
		testutil.FailErr(t, "read original member", err)
		if index == 0 {
			switch kind {
			case "traversal":
				header.Name = "../escaped"
			case "absolute":
				header.Name = "/escaped"
			case "symlink":
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "/outside"
				header.Size = 0
				payload = nil
			case "hardlink":
				header.Typeflag = tar.TypeLink
				header.Linkname = "opengrep"
				header.Size = 0
				payload = nil
			case "directory":
				header.Typeflag = tar.TypeDir
				header.Size = 0
				payload = nil
			case "extra":
				header.Name = "unreviewed"
			case "missing":
				index++
				continue
			case "unsafe mode":
				header.Mode = 0o4755
			}
		}
		testutil.FailErr(t, "write changed header", writer.WriteHeader(header))
		_, err = writer.Write(payload)
		testutil.FailErr(t, "write changed payload", err)
		if index == 0 && kind == "duplicate" {
			testutil.FailErr(t, "write duplicate header", writer.WriteHeader(header))
			_, err = writer.Write(payload)
			testutil.FailErr(t, "write duplicate payload", err)
		}
		index++
	}
	testutil.FailErr(t, "close changed tar", writer.Close())
	return output.Bytes()
}

func TestReleaseRejectsMalformedArchiveEvenWhenOuterPinMatches(t *testing.T) {
	for _, kind := range []string{"traversal", "absolute", "symlink", "hardlink", "directory", "extra", "missing", "duplicate", "unsafe mode", "missing tar end", "trailing tar payload", "unaligned tar padding", "truncated gzip", "concatenated gzip"} {
		t.Run(kind, func(t *testing.T) {
			f := newReleaseFixture(t)
			var raw []byte
			switch kind {
			case "truncated gzip":
				raw = f.raw[:len(f.raw)-4]
			case "concatenated gzip":
				raw = append(append([]byte{}, f.raw...), f.raw...)
			default:
				raw = alterReleaseTar(t, f.raw, kind)
			}
			f.pinArchive(t, raw)
			root := t.TempDir()
			_, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, bundled.ReleaseFetchOptions{CacheRoot: root, Archive: f.archive})
			if err == nil {
				t.Fatal("malformed archive accepted")
			}
			pointers, err := filepath.Glob(filepath.Join(root, "*.current"))
			testutil.FailErr(t, "inspect failed publication", err)
			if len(pointers) != 0 {
				t.Fatal("malformed archive published")
			}
		})
	}
}

func TestReleaseQualificationFailureDoesNotPublishAndRetryCanRecover(t *testing.T) {
	f := newReleaseFixture(t)
	root := t.TempDir()
	original := f.source.OpenGrep.SourceManifestSHA256
	f.source.OpenGrep.SourceManifestSHA256 = buildDigest([]byte("unqualified source inventory"))
	if _, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, bundled.ReleaseFetchOptions{CacheRoot: root, Archive: f.archive}); err == nil {
		t.Fatal("unqualified source admitted")
	}
	pointers, err := filepath.Glob(filepath.Join(root, "*.current"))
	testutil.FailErr(t, "inspect unqualified publication", err)
	if len(pointers) != 0 {
		t.Fatal("unqualified artifact published")
	}
	f.source.OpenGrep.SourceManifestSHA256 = original
	f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: root, Offline: true})
}

func TestReleaseCacheRejectsEscapingPointersAndSymlinks(t *testing.T) {
	for _, kind := range []string{"traversing pointer", "pointer symlink", "generation symlink", "archive symlink", "import symlink", "lock symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newReleaseFixture(t)
			root := t.TempDir()
			resolved := f.fetch(t, bundled.ReleaseFetchOptions{CacheRoot: root, Archive: f.archive})
			digest := f.source.OpenGrep.Artifacts[0].SHA256
			options := bundled.ReleaseFetchOptions{CacheRoot: root, Offline: true}
			switch kind {
			case "traversing pointer":
				testutil.FailErr(t, "write escaping pointer", os.WriteFile(filepath.Join(root, digest+".current"), []byte("../outside"), 0o600))
			case "pointer symlink":
				replaceWithReleaseSymlink(t, filepath.Join(root, digest+".current"), f.archive)
			case "generation symlink":
				testutil.FailErr(t, "remove fixture generation", os.RemoveAll(resolved.Directory))
				testutil.FailErr(t, "symlink generation", os.Symlink(f.directory, resolved.Directory))
			case "archive symlink":
				replaceWithReleaseSymlink(t, filepath.Join(root, digest+".tar.gz"), f.archive)
			case "import symlink":
				link := filepath.Join(t.TempDir(), "archive-link")
				testutil.FailErr(t, "symlink import", os.Symlink(f.archive, link))
				options.Archive = link
			case "lock symlink":
				replaceWithReleaseSymlink(t, filepath.Join(root, digest+".lock"), f.archive)
			}
			if _, err := bundled.FetchReleaseArtifact(t.Context(), f.source, f.goos, f.goarch, options); err == nil {
				t.Fatal("unsafe cache path accepted")
			}
		})
	}
}

func replaceWithReleaseSymlink(t *testing.T, path, target string) {
	t.Helper()
	testutil.FailErr(t, "remove fixture file", os.Remove(path))
	testutil.FailErr(t, "symlink fixture file", os.Symlink(target, path))
}
