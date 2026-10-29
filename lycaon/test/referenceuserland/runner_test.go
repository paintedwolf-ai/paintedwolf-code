//go:build unix

package referenceuserland

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The test binary stands in for the Docker CLI when these are set.
const (
	fakeDockerState    = "PW_REFERENCE_FAKE_DOCKER"
	fakeDockerTree     = "PW_REFERENCE_FAKE_TREE"
	fakeDockerPlatform = "PW_REFERENCE_FAKE_PLATFORM"
	fakeDockerFail     = "PW_REFERENCE_FAKE_FAIL"
)

func TestMain(m *testing.M) {
	if state := os.Getenv(fakeDockerState); state != "" {
		os.Exit(fakeDocker(state, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

type fakeImage struct {
	Arch string `json:"arch"`
	Tree string `json:"tree"`
}

// fakeDocker answers the Docker CLI calls the runner makes, keeping images and
// containers as files under state. Each container's root holds one marker file
// whose content is the image's tree text, so a changed build changes the digest.
func fakeDocker(state string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	calls, err := os.OpenFile(filepath.Join(state, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = fmt.Fprintln(calls, strings.Join(args, " "))
		_ = calls.Close()
	}
	if args[0] == os.Getenv(fakeDockerFail) {
		_, _ = fmt.Fprintln(stderr, "fake failure of", args[0])
		return 1
	}
	image := func(tag string) string { return filepath.Join(state, "images", strings.ReplaceAll(tag, ":", "=")) }
	readImage := func(tag string) (fakeImage, error) {
		var img fakeImage
		data, err := os.ReadFile(image(tag))
		if err == nil {
			err = json.Unmarshal(data, &img)
		}
		return img, err
	}
	writeImage := func(tag string, img fakeImage) error {
		data, _ := json.Marshal(img)
		if err := os.MkdirAll(filepath.Join(state, "images"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(image(tag), data, 0o644)
	}
	last := args[len(args)-1]
	switch {
	case args[0] == "version":
		_, _ = fmt.Fprintln(stdout, runtime.GOARCH)
	case args[0] == "build":
		platform, tag := args[slices.Index(args, "--platform")+1], args[slices.Index(args, "--tag")+1]
		if override := os.Getenv(fakeDockerPlatform); override != "" {
			platform = override
		}
		if _, err := io.ReadAll(stdin); err != nil || last != "-" {
			return 1
		}
		if err := writeImage(tag, fakeImage{Arch: strings.TrimPrefix(platform, "linux/"), Tree: os.Getenv(fakeDockerTree)}); err != nil {
			return 1
		}
	case args[0] == "image" && args[1] == "inspect":
		img, err := readImage(last)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "No such image:", last)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, strings.NewReplacer("{{.Id}}", "sha256:fake", "{{.Os}}", "linux", "{{.Architecture}}", img.Arch).Replace(args[3]))
	case args[0] == "image" && args[1] == "rm":
		if os.Remove(image(last)) != nil {
			return 1
		}
	case args[0] == "tag":
		img, err := readImage(args[1])
		if err != nil || writeImage(args[2], img) != nil {
			return 1
		}
	case args[0] == "create":
		img, err := readImage(args[len(args)-2])
		if err != nil {
			return 1
		}
		id := fmt.Sprintf("container-%d", len(must(os.ReadDir(state))))
		root := filepath.Join(state, id)
		if os.MkdirAll(filepath.Join(root, "etc"), 0o755) != nil || os.WriteFile(filepath.Join(root, "etc", "marker"), []byte(img.Tree), 0o644) != nil {
			return 1
		}
		_, _ = fmt.Fprintln(stdout, id)
	case args[0] == "cp":
		if _, err := os.Stat(args[1]); err != nil {
			return 1
		}
	case args[0] == "start":
		if err := serve(filepath.Join(state, last), stdin, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
	case args[0] == "rm":
		if os.RemoveAll(filepath.Join(state, last)) != nil {
			return 1
		}
	default:
		_, _ = fmt.Fprintln(stderr, "unexpected call", args)
		return 2
	}
	return 0
}

func must[T any](value T, _ error) T { return value }

// fakeRunner returns a runner whose Docker CLI is this test binary.
func fakeRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	state := t.TempDir()
	t.Setenv(fakeDockerState, state)
	r, err := newRunner(t.Context(), os.Args[0])
	testutil.FailErr(t, "new runner", err)
	t.Cleanup(func() { _ = r.Close() })
	return r, state
}

func fakeCalls(t *testing.T, state, command string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(state, "calls"))
	testutil.FailErr(t, "read calls", err)
	var matched []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, command+" ") {
			matched = append(matched, line)
		}
	}
	return matched
}

func imagePresent(state, tag string) bool {
	_, err := os.Stat(filepath.Join(state, "images", strings.ReplaceAll(tag, ":", "=")))
	return err == nil
}

func installVerifiedImage(t *testing.T, r *Runner, state string, id ID) {
	t.Helper()
	tag, err := r.manifest.Userlands[id].Tag(r.arch)
	testutil.FailErr(t, "tag", err)
	data, err := json.Marshal(fakeImage{Arch: r.arch})
	testutil.FailErr(t, "encode image", err)
	testutil.FailErr(t, "mkdir images", os.MkdirAll(filepath.Join(state, "images"), 0o755))
	testutil.FailErr(t, "write image", os.WriteFile(filepath.Join(state, "images", strings.ReplaceAll(tag, ":", "=")), data, 0o644))
}

func TestRunnerRunsEachBatchInOneOfflineContainer(t *testing.T) {
	r, state := fakeRunner(t)
	installVerifiedImage(t, r, state, BusyBox)
	results, err := r.Run(t.Context(), BusyBox, []Case{
		{Program: `printf 'a\n' > made.txt; printf out; exit 3`, Tracked: []string{"made.txt"}},
		{Program: `printf err >&2`, Workspace: map[string]Entry{"seed": {Data: []byte("s")}}, Tracked: []string{"made.txt", "seed"}},
	})
	testutil.FailErr(t, "run", err)
	if len(results) != 2 || results[0].Status != 3 || string(results[0].Stdout) != "out" ||
		string(results[0].Files["made.txt"].Data) != "a\n" || string(results[1].Stderr) != "err" ||
		results[1].Files["made.txt"] != nil || string(results[1].Files["seed"].Data) != "s" {
		t.Fatalf("results = %+v", results)
	}
	creates := fakeCalls(t, state, "create")
	if len(creates) != 1 {
		t.Fatalf("created %d containers, want one per batch", len(creates))
	}
	for _, flag := range []string{"--pull never", "--platform linux/" + r.arch, "--network none", "--hostname reference",
		"--user 0:0", "--interactive", "--entrypoint " + driverPath} {
		if !strings.Contains(creates[0], flag) {
			t.Errorf("container created without %q: %s", flag, creates[0])
		}
	}
	container := strings.Fields(fakeCalls(t, state, "start")[0])
	id := container[len(container)-1]
	if cps := fakeCalls(t, state, "cp"); len(cps) != 1 || !strings.HasSuffix(cps[0], " "+id+":"+driverPath) {
		t.Fatalf("driver copies = %q, want one into %s", cps, id)
	}
	if rms := fakeCalls(t, state, "rm"); !slices.Equal(rms, []string{"rm --force " + id}) {
		t.Fatalf("removals = %q, want the batch container removed", rms)
	}
}

func TestRunnerRefusesWithoutTheVerifiedImageOrValidCases(t *testing.T) {
	r, state := fakeRunner(t)
	if _, err := r.Run(t.Context(), GNU, []Case{{Program: "true"}}); err == nil || !strings.Contains(err.Error(), "userlands:build") {
		t.Fatalf("run without an image = %v, want a build instruction", err)
	}
	if _, err := r.Run(t.Context(), GNU, []Case{{Program: "true", Tracked: []string{"/etc/passwd"}}}); err == nil {
		t.Fatal("run accepted a tracked path outside the workspace")
	}
	if _, err := r.Run(t.Context(), ID("toybox"), nil); err == nil {
		t.Fatal("run accepted an unknown userland")
	}
	if creates := fakeCalls(t, state, "create"); len(creates) != 0 {
		t.Fatalf("refused runs created containers: %q", creates)
	}
}

func TestRunnerRemovesTheContainerWhenTheBatchFails(t *testing.T) {
	r, state := fakeRunner(t)
	installVerifiedImage(t, r, state, BusyBox)
	t.Setenv(fakeDockerFail, "start")
	if _, err := r.Run(t.Context(), BusyBox, []Case{{Program: "true"}}); err == nil || !strings.Contains(err.Error(), "fake failure of start") {
		t.Fatalf("run = %v, want the start failure", err)
	}
	if rms := fakeCalls(t, state, "rm"); len(rms) != 1 {
		t.Fatalf("removals = %q, want the failed container removed", rms)
	}
}

func TestBuildTagsOnlyAnImageThatReproducesItsRecordedTree(t *testing.T) {
	r, state := fakeRunner(t)
	u := r.manifest.Userlands[BusyBox]
	recorded, err := r.Record(t.Context(), BusyBox, r.arch)
	testutil.FailErr(t, "record", err)
	tag, err := u.Tag(r.arch)
	testutil.FailErr(t, "tag", err)
	candidate := imageRepository + ":busybox-" + r.arch + "-candidate"
	if u.Trees[r.arch] != recorded.Tree || !imagePresent(state, tag) || imagePresent(state, candidate) {
		t.Fatalf("record left tree %s (inventory %s), tag present %v, candidate present %v",
			u.Trees[r.arch], recorded.Tree, imagePresent(state, tag), imagePresent(state, candidate))
	}
	if builds := fakeCalls(t, state, "build"); len(builds) != 1 || !strings.Contains(builds[0], "--no-cache") ||
		!strings.Contains(builds[0], "--build-arg "+u.BuildArgs(r.arch)[0]) {
		t.Fatalf("record builds = %q, want one uncached build with the pins", builds)
	}

	inv, err := r.Build(t.Context(), BusyBox)
	testutil.FailErr(t, "verify existing image", err)
	if inv.Tree != recorded.Tree || len(fakeCalls(t, state, "build")) != 1 {
		t.Fatal("an existing verified image was rebuilt or reported a different tree")
	}

	testutil.FailErr(t, "drop image", os.Remove(filepath.Join(state, "images", strings.ReplaceAll(tag, ":", "="))))
	_, err = r.Build(t.Context(), BusyBox)
	testutil.FailErr(t, "rebuild", err)
	if !imagePresent(state, tag) || imagePresent(state, candidate) {
		t.Fatal("a reproduced build was not tagged, or its candidate stayed")
	}

	testutil.FailErr(t, "drop image", os.Remove(filepath.Join(state, "images", strings.ReplaceAll(tag, ":", "="))))
	t.Setenv(fakeDockerTree, "drifted")
	if _, err := r.Build(t.Context(), BusyBox); err == nil || !strings.Contains(err.Error(), "did not reproduce") {
		t.Fatalf("build of a drifted image = %v, want a reproduction failure", err)
	}
	if imagePresent(state, tag) || imagePresent(state, candidate) {
		t.Fatal("a drifted build left an image tagged")
	}
}

func TestRecordRefusesAnImageOfAnotherPlatform(t *testing.T) {
	r, state := fakeRunner(t)
	before := r.manifest.Userlands[BusyBox].Trees[r.arch]
	t.Setenv(fakeDockerPlatform, "linux/s390x")
	if _, err := r.Record(t.Context(), BusyBox, r.arch); err == nil || !strings.Contains(err.Error(), "not linux/"+r.arch) {
		t.Fatalf("record = %v, want a platform refusal", err)
	}
	if r.manifest.Userlands[BusyBox].Trees[r.arch] != before || imagePresent(state, imageRepository+":busybox-"+r.arch+"-candidate") {
		t.Fatal("a refused record changed the manifest or kept its candidate")
	}
	if _, err := r.Record(t.Context(), ID("toybox"), r.arch); err == nil {
		t.Fatal("record accepted an unknown userland")
	}
}

func TestInventoryDescribesTheVerifiedImage(t *testing.T) {
	r, state := fakeRunner(t)
	installVerifiedImage(t, r, state, GNU)
	inv, err := r.Inventory(t.Context(), GNU)
	testutil.FailErr(t, "inventory", err)
	if inv.Architecture != r.arch || inv.Entries != 3 || !treePattern.MatchString(inv.Tree) {
		t.Fatalf("inventory = %+v, want the container's root, etc and marker", inv)
	}
	if r.Architecture() != runtime.GOARCH || r.Manifest() != r.manifest {
		t.Fatal("runner reports another architecture or manifest")
	}
	if _, err := r.Inventory(t.Context(), BSD); err == nil {
		t.Fatal("inventory of an unbuilt image succeeded")
	}
}

func TestNewRunnerNeedsTheContainerClient(t *testing.T) {
	if _, err := newRunner(t.Context(), filepath.Join(t.TempDir(), "docker")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("runner without a client = %v, want not found", err)
	}
	t.Setenv(fakeDockerState, t.TempDir())
	t.Setenv(fakeDockerFail, "version")
	if _, err := newRunner(context.Background(), os.Args[0]); err == nil {
		t.Fatal("runner started without a daemon")
	}
}
