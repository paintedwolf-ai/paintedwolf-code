//go:build unix

package referenceuserland

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandRefusesMisuseBeforeReachingDocker(t *testing.T) {
	opened := false
	open := func(context.Context) (*Runner, error) {
		opened = true
		return nil, errors.New("no daemon")
	}
	for _, args := range [][]string{
		nil,
		{"publish"},
		{"build", "toybox"},
		{"build", "-arch", "amd64"},
		{"record", "-unknown"},
		{"inventory"},
		{"inventory", "gnu", "bsd"},
	} {
		var stderr bytes.Buffer
		if got := runCommand(t.Context(), args, strings.NewReader(""), &bytes.Buffer{}, &stderr, open); got != exitUsage || stderr.Len() == 0 {
			t.Errorf("command %q = %d with %q, want %d and a usage message", args, got, stderr.String(), exitUsage)
		}
	}
	if opened {
		t.Fatal("a refused invocation reached the container runtime")
	}
	var stderr bytes.Buffer
	if got := runCommand(t.Context(), []string{"build"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, open); got != exitUnverified ||
		!strings.Contains(stderr.String(), "unverified: no daemon") {
		t.Fatalf("command without a daemon = %d with %q, want unverified", got, stderr.String())
	}
}

func TestCommandDriverAnswersOrFailsOnItsRequest(t *testing.T) {
	var stderr bytes.Buffer
	if got := Command(t.Context(), []string{"driver"}, strings.NewReader("{"), &bytes.Buffer{}, &stderr); got != exitFailed ||
		!strings.Contains(stderr.String(), "decode request") {
		t.Fatalf("driver = %d with %q, want a decode failure", got, stderr.String())
	}
}

func TestCommandRecordsBuildsAndDescribesAUserland(t *testing.T) {
	r, _ := fakeRunner(t)
	_, err := r.driver(t.Context(), r.arch)
	testutil.FailErr(t, "build the driver inside the module", err)
	// Each invocation opens and closes its own runner; they share the manifest and the
	// driver, as one process would.
	open := func(ctx context.Context) (*Runner, error) {
		next, err := newRunner(ctx, os.Args[0])
		if err == nil {
			next.manifest, next.drivers = r.manifest, r.drivers
		}
		return next, err
	}
	module := t.TempDir()
	manifest := filepath.Join(module, filepath.FromSlash(ManifestPath))
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(manifest), 0o755))
	testutil.FailErr(t, "seed manifest", os.WriteFile(manifest, []byte("{}"), 0o644))
	t.Chdir(module)
	invoke := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := runCommand(t.Context(), args, strings.NewReader(""), &stdout, &stderr, open)
		return code, stdout.String(), stderr.String()
	}

	if code, out, errOut := invoke("record", "-arch", r.arch, "busybox"); code != 0 || !strings.Contains(out, "userland busybox ("+r.arch+"): recorded sha256:") {
		t.Fatalf("record = %d %q %q", code, out, errOut)
	}
	data, err := os.ReadFile(manifest)
	testutil.FailErr(t, "read recorded manifest", err)
	written, err := ParseManifest(data)
	testutil.FailErr(t, "parse recorded manifest", err)
	if tree := r.manifest.Userlands[BusyBox].Trees[r.arch]; written.Userlands[BusyBox].Trees[r.arch] != tree {
		t.Fatalf("written tree %s, want the recorded %s", written.Userlands[BusyBox].Trees[r.arch], tree)
	}

	if code, out, errOut := invoke("build", "busybox"); code != 0 || !strings.Contains(out, "userland busybox ("+r.arch+"): verified sha256:") {
		t.Fatalf("build = %d %q %q", code, out, errOut)
	}
	code, out, errOut := invoke("inventory", "busybox")
	var inv Inventory
	if code != 0 || json.Unmarshal([]byte(out), &inv) != nil || inv.Tree != r.manifest.Userlands[BusyBox].Trees[r.arch] {
		t.Fatalf("inventory = %d %q %q", code, out, errOut)
	}
	if code, _, errOut := invoke("record", "-arch", "riscv64", "busybox"); code != exitFailed || !strings.Contains(errOut, "unknown architecture") {
		t.Fatalf("record of an unknown architecture = %d %q", code, errOut)
	}
	if code, _, errOut := invoke("build", "gnu"); code != exitFailed || !strings.Contains(errOut, "did not reproduce") {
		t.Fatalf("build of an image that does not match its record = %d %q", code, errOut)
	}
	t.Chdir(t.TempDir())
	if code, _, errOut := invoke("record", "busybox"); code != exitFailed || !strings.Contains(errOut, "Go module root") {
		t.Fatalf("record outside the module = %d %q", code, errOut)
	}
}
