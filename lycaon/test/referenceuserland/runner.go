package referenceuserland

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// driverPath is where each container receives the driver binary.
	driverPath = "/.reference-driver"
	// driverScratch holds the driver's captures, out of the program's reach.
	driverScratch = "/run/reference-driver"
	driverPackage = "github.com/lycaon/lycaon/test/referenceuserland/cmd/userlands"
)

type request struct {
	Inventory bool   `json:"inventory,omitempty"`
	Shell     string `json:"shell,omitempty"`
	Cases     []Case `json:"cases,omitempty"`
}

// Runner runs programs in the reference userland images through the Docker CLI.
// Each batch runs in one new container: the driver, built from this package for the
// image's architecture, resets the fixed environment before every program.
type Runner struct {
	runtime  string
	manifest *Manifest
	arch     string
	scratch  string
	drivers  map[string]string
	// Log receives image build output.
	Log io.Writer
}

// NewRunner returns a runner for the Docker daemon's architecture. Close removes
// the drivers it builds.
func NewRunner(ctx context.Context) (*Runner, error) {
	return newRunner(ctx, "docker")
}

func newRunner(ctx context.Context, runtime string) (*Runner, error) {
	manifest, err := LoadManifest()
	if err != nil {
		return nil, err
	}
	path, err := exec.LookPath(runtime)
	if err != nil {
		return nil, fmt.Errorf("reference userlands need Docker: %w", err)
	}
	r := &Runner{runtime: path, manifest: manifest, drivers: map[string]string{}, Log: io.Discard}
	out, err := r.command(ctx, nil, "version", "--format", "{{.Server.Arch}}")
	if err != nil {
		return nil, err
	}
	r.arch = strings.TrimSpace(string(out))
	if r.scratch, err = os.MkdirTemp("", "reference-userland-"); err != nil {
		return nil, err
	}
	return r, nil
}

// Close removes the runner's scratch files.
func (r *Runner) Close() error {
	return os.RemoveAll(r.scratch)
}

// Architecture is the platform of the images this runner uses.
func (r *Runner) Architecture() string {
	return r.arch
}

// Manifest returns the pinned userland definitions the runner builds and runs.
func (r *Runner) Manifest() *Manifest {
	return r.manifest
}

// Run runs each case's program in the userland's verified image and returns one
// result per case, in order.
func (r *Runner) Run(ctx context.Context, id ID, cases []Case) ([]Result, error) {
	for i, c := range cases {
		if err := c.validate(); err != nil {
			return nil, fmt.Errorf("case %d: %w", i, err)
		}
	}
	u, image, err := r.verifiedImage(ctx, id)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(cases))
	err = r.exchange(ctx, image, r.arch, request{Shell: u.Shell, Cases: cases}, func(d *json.Decoder) error {
		for range cases {
			var result Result
			if err := d.Decode(&result); err != nil {
				return fmt.Errorf("decode result %d: %w", len(results), err)
			}
			results = append(results, result)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("run in %s: %w", id, err)
	}
	return results, nil
}

// Inventory describes the userland's verified image.
func (r *Runner) Inventory(ctx context.Context, id ID) (Inventory, error) {
	_, image, err := r.verifiedImage(ctx, id)
	if err != nil {
		return Inventory{}, err
	}
	return r.inventory(ctx, image, r.arch)
}

func (r *Runner) verifiedImage(ctx context.Context, id ID) (*Userland, string, error) {
	u, err := r.manifest.Userland(id)
	if err != nil {
		return nil, "", err
	}
	tag, err := u.Tag(r.arch)
	if err != nil {
		return nil, "", err
	}
	if !r.imageExists(ctx, tag) {
		return nil, "", fmt.Errorf("image %s is not built; run ./task userlands:build", tag)
	}
	return u, tag, nil
}

func (r *Runner) inventory(ctx context.Context, image, arch string) (Inventory, error) {
	var inv Inventory
	err := r.exchange(ctx, image, arch, request{Inventory: true}, func(d *json.Decoder) error {
		return d.Decode(&inv)
	})
	return inv, err
}

// exchange sends one request to the driver in a new container of image and decodes
// the answer. The container has no network and is always removed.
func (r *Runner) exchange(ctx context.Context, image, arch string, req request, decode func(*json.Decoder) error) (err error) {
	driver, err := r.driver(ctx, arch)
	if err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	out, err := r.command(ctx, nil, "create", "--pull", "never", "--platform", "linux/"+arch,
		"--network", "none", "--hostname", referenceHost, "--user", "0:0", "--interactive",
		"--entrypoint", driverPath, image, "driver")
	if err != nil {
		return err
	}
	container := strings.TrimSpace(string(out))
	defer func() {
		_, removeErr := r.command(context.WithoutCancel(ctx), nil, "rm", "--force", container)
		err = errors.Join(err, removeErr)
	}()
	if _, err := r.command(ctx, nil, "cp", driver, container+":"+driverPath); err != nil {
		return err
	}
	out, err = r.command(ctx, bytes.NewReader(body), "start", "--attach", "--interactive", container)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(out))
	if err := decode(decoder); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("the driver answered more than was asked")
	}
	return nil
}

// driver builds this package's command for the container's architecture once per runner.
func (r *Runner) driver(ctx context.Context, arch string) (string, error) {
	if built, ok := r.drivers[arch]; ok {
		return built, nil
	}
	out := filepath.Join(r.scratch, "driver-"+arch)
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", out, driverPackage)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build the %s driver: %w: %s", arch, err, output)
	}
	r.drivers[arch] = out
	return out, nil
}

func (r *Runner) imageExists(ctx context.Context, tag string) bool {
	_, err := r.command(ctx, nil, "image", "inspect", "--format", "{{.Id}}", tag)
	return err == nil
}

func (r *Runner) command(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.runtime, args...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
