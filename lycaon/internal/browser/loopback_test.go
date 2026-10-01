package browser

import (
	"errors"
	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsLoopbackURL(t *testing.T) {
	ok := []string{
		"http://127.0.0.1:5173/",
		"https://localhost/app",
		"http://[::1]:8080/",
	}
	for _, u := range ok {
		if !IsLoopbackURL(u) {
			t.Fatalf("expected loopback: %s", u)
		}
	}
	bad := []string{
		"http://example.com/",
		"http://192.168.1.1/",
		"file:///tmp/x",
		"not-a-url",
	}
	for _, u := range bad {
		if IsLoopbackURL(u) {
			t.Fatalf("expected non-loopback: %s", u)
		}
	}
}

func TestResolveCaptureTarget_rejectsNonLoopback(t *testing.T) {
	_, err := resolveCaptureTarget(CaptureRequest{URL: "http://example.com/"})
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_URL_NOT_LOOPBACK" {
		t.Fatalf("got %#v want CAPTURE_URL_NOT_LOOPBACK", err)
	}
}

func TestResolveCaptureTarget_projectDirNoListen(t *testing.T) {
	dir := t.TempDir()
	target, err := resolveCaptureTarget(CaptureRequest{ProjectDir: dir})
	testutil.FailErr(t, "resolveCaptureTarget failed", err)
	if target.RootDir == "" || target.URL != StaticOrigin {
		t.Fatalf("got %#v", target)
	}
}

func TestResolveCaptureTarget_projectDirPath(t *testing.T) {
	dir := t.TempDir()
	target, err := resolveCaptureTarget(CaptureRequest{ProjectDir: dir, Path: "/talks/"})
	testutil.FailErr(t, "resolveCaptureTarget failed", err)
	if target.URL != "http://lycaon.capture/talks/" {
		t.Fatalf("url=%q", target.URL)
	}
	if target.Origin != StaticOrigin || target.RootDir == "" {
		t.Fatalf("got %#v", target)
	}
}

func TestResolveCaptureTarget_pathWithoutProjectDir(t *testing.T) {
	_, err := resolveCaptureTarget(CaptureRequest{Path: "/talks/"})
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "CAPTURE_TARGET_INVALID" {
		t.Fatalf("got %#v", err)
	}
}

func TestJoinStaticEntry(t *testing.T) {
	got, err := JoinStaticEntry("")
	if err != nil || got != StaticOrigin {
		t.Fatalf("empty: %q %v", got, err)
	}
	got, err = JoinStaticEntry("talks")
	if err != nil || got != "http://lycaon.capture/talks" {
		t.Fatalf("talks: %q %v", got, err)
	}
	got, err = JoinStaticEntry("/talks/")
	if err != nil || got != "http://lycaon.capture/talks/" {
		t.Fatalf("slash: %q %v", got, err)
	}
	if _, err := JoinStaticEntry("https://evil.example/"); err == nil {
		t.Fatal("expected reject for absolute url")
	}
	if _, err := JoinStaticEntry("../escape"); err == nil {
		t.Fatal("expected reject for escape")
	}
}

func TestResolveStaticFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "css"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(root, "css", "app.css"), []byte("body{}"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "talks"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(root, "talks", "index.html"), []byte("<html/>"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html/>"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	full, ok := resolveStaticFile(root, "/css/app.css")
	if !ok || filepath.Base(full) != "app.css" {
		t.Fatalf("css: %s ok=%v", full, ok)
	}
	full, ok = resolveStaticFile(root, "/talks/")
	if !ok || !strings.HasSuffix(full, filepath.Join("talks", "index.html")) {
		t.Fatalf("dir index: %s ok=%v", full, ok)
	}
	full, ok = resolveStaticFile(root, "/talks")
	if !ok || !strings.HasSuffix(full, filepath.Join("talks", "index.html")) {
		t.Fatalf("dir no slash: %s ok=%v", full, ok)
	}
	full, ok = resolveStaticFile(root, "/")
	if !ok || filepath.Base(full) != "index.html" {
		t.Fatalf("root: %s ok=%v", full, ok)
	}
	if _, ok := resolveStaticFile(root, "/missing.css"); ok {
		t.Fatal("missing should fail")
	}
	if _, ok := resolveStaticFile(root, "/../etc/passwd"); ok {
		t.Fatal("escape should fail")
	}
}

func TestSafeJoinUnderRoot(t *testing.T) {
	root := t.TempDir()
	full, ok := safeJoinUnderRoot(root, "index.html")
	if !ok || filepath.Base(full) != "index.html" {
		t.Fatalf("join failed: %s ok=%v", full, ok)
	}
	if _, ok := safeJoinUnderRoot(root, "../etc/passwd"); ok {
		t.Fatal("escape should fail")
	}
}

func TestSafeJoinUnderRoot_rejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests are unreliable on Windows CI")
	}
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	link := filepath.Join(root, "leak")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("filesystem disallows symlinks: %v", err)
	}
	if _, ok := safeJoinUnderRoot(root, "leak"); ok {
		t.Fatal("symlink escape should fail")
	}
}

func TestSafeJoinUnderRoot_allowsInTreeSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests are unreliable on Windows CI")
	}
	root := t.TempDir()
	target := filepath.Join(root, "assets", "ok.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(target, []byte("ok"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	link := filepath.Join(root, "public", "ok.txt")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("filesystem disallows symlinks: %v", err)
	}
	full, ok := safeJoinUnderRoot(root, "public/ok.txt")
	if !ok {
		t.Fatal("in-tree symlink should be allowed")
	}
	want, err := filepath.EvalSymlinks(target)
	testutil.FailErr(t, "filepath.EvalSymlinks failed", err)
	if full != want {
		t.Fatalf("got %q want resolved %q", full, want)
	}
}

func TestPrepareStaticRoot_resolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	got, err := PrepareStaticRoot(root)
	testutil.FailErr(t, "PrepareStaticRoot failed", err)
	want, err := filepath.EvalSymlinks(root)
	testutil.FailErr(t, "filepath.EvalSymlinks failed", err)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
