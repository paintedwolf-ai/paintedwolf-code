package commandconformance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

type fakeImages struct {
	fakeReferences
	manifest *referenceuserland.Manifest
	closed   bool
}

func (f *fakeImages) Manifest() *referenceuserland.Manifest { return f.manifest }
func (f *fakeImages) Architecture() string                  { return "arm64" }
func (f *fakeImages) Close() error                          { f.closed = true; return nil }

// captureFixtureRoot gives the test a module root holding the data files capture rewrites.
func captureFixtureRoot(t *testing.T, snapshots string) string {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "create testdata", os.MkdirAll(filepath.Join(root, filepath.Dir(SnapshotsPath)), 0o755))
	testutil.FailErr(t, "write snapshots", os.WriteFile(filepath.Join(root, SnapshotsPath), []byte(snapshots), 0o644))
	testutil.FailErr(t, "write seed", os.WriteFile(filepath.Join(root, GitSeedPath), []byte("{}\n"), 0o644))
	t.Chdir(root)
	return root
}

func TestCommandReportsUsageErrors(t *testing.T) {
	never := func(context.Context) (imageRunner, error) {
		t.Fatal("a usage error opened the runner")
		return nil, nil
	}
	captureFixtureRoot(t, "{}")
	for _, args := range [][]string{nil, {"record"}, {"capture", "-unknown"}, {"capture", "no-such-case"}} {
		var stderr bytes.Buffer
		if got := runCommand(t.Context(), args, &bytes.Buffer{}, &stderr, never); got != exitUsage || stderr.Len() == 0 {
			t.Errorf("args %q: status %d, stderr %q", args, got, stderr.String())
		}
	}
	t.Chdir(t.TempDir())
	var stderr bytes.Buffer
	if got := runCommand(t.Context(), []string{"capture"}, &bytes.Buffer{}, &stderr, never); got != exitUsage || !strings.Contains(stderr.String(), "module root") {
		t.Fatalf("capture outside the module root: %d %q", got, stderr.String())
	}
}

func TestCommandReportsAnUnavailableRunnerAsUnverified(t *testing.T) {
	captureFixtureRoot(t, "{}")
	var stderr bytes.Buffer
	open := func(context.Context) (imageRunner, error) { return nil, errors.New("no Docker") }
	if got := runCommand(t.Context(), []string{"capture"}, &bytes.Buffer{}, &stderr, open); got != exitUnverified || !strings.Contains(stderr.String(), "unverified: no Docker") {
		t.Fatalf("status %d, stderr %q", got, stderr.String())
	}
}

func TestCaptureStoresSnapshotsFromTheRunnersImages(t *testing.T) {
	manifest, err := referenceuserland.LoadManifest()
	testutil.FailErr(t, "load the manifest", err)
	root := captureFixtureRoot(t, `{"userlands": {}, "cases": {"retired": {"gnu": {"input": "x", "status": 0, "stdout": "", "stderr": ""}}}}`)
	images := &fakeImages{fakeReferences: fakeReferences{answer: echoProgram}, manifest: manifest}
	open := func(context.Context) (imageRunner, error) { return images, nil }

	// Snapshots from other images are only replaced by a complete capture.
	var stderr bytes.Buffer
	if got := runCommand(t.Context(), []string{"capture", Cases()[0].ID}, &bytes.Buffer{}, &stderr, open); got != exitFailed || !strings.Contains(stderr.String(), "capture every case") {
		t.Fatalf("partial capture over other images: %d %q", got, stderr.String())
	}
	var stdout bytes.Buffer
	if got := runCommand(t.Context(), []string{"capture"}, &stdout, &stderr, open); got != 0 || !images.closed {
		t.Fatalf("capture: %d %q", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "cases stored") {
		t.Fatalf("capture said %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(root, SnapshotsPath))
	testutil.FailErr(t, "read stored snapshots", err)
	stored, err := decodeSnapshots(data)
	testutil.FailErr(t, "decode stored snapshots", err)
	if !imagesEqual(stored.Userlands, Images(manifest)) || len(stored.Cases) != len(Cases()) || stored.Cases["retired"] != nil {
		t.Fatalf("stored %d cases from %v", len(stored.Cases), stored.Userlands)
	}
	first := Cases()[0]
	if _, err := stored.For(first, GitSeed{}); err != nil {
		testutil.FailErr(t, "stored.For failed", err)
	}

	// A partial capture over the same images keeps every other case.
	images.answer = func(referenceuserland.Case) referenceuserland.Result { return referenceuserland.Result{Status: 3} }
	if got := runCommand(t.Context(), []string{"capture", first.ID}, &stdout, &stderr, open); got != 0 {
		t.Fatalf("partial capture: %d %q", got, stderr.String())
	}
	data, err = os.ReadFile(filepath.Join(root, SnapshotsPath))
	testutil.FailErr(t, "read stored snapshots", err)
	stored, err = decodeSnapshots(data)
	testutil.FailErr(t, "decode stored snapshots", err)
	if len(stored.Cases) != len(Cases()) || stored.Cases[first.ID][first.References[0]].Status != 3 || stored.Cases[Cases()[1].ID][referenceuserland.GNU].Status != 0 {
		t.Fatalf("partial capture stored %+v", stored.Cases[first.ID])
	}

	images.err = errors.New("lost the daemon")
	if got := runCommand(t.Context(), []string{"capture", "-git-seed"}, &stdout, &stderr, open); got != exitFailed {
		t.Fatalf("failed capture exited %d", got)
	}
}
