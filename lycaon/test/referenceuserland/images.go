package referenceuserland

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Build makes sure the userland's verified image exists for the runner's
// architecture. A missing image is built from its pins and tagged only once its
// tree hashes to the recorded digest; an existing image is verified again.
func (r *Runner) Build(ctx context.Context, id ID) (Inventory, error) {
	u, err := r.manifest.Userland(id)
	if err != nil {
		return Inventory{}, err
	}
	tag, err := u.Tag(r.arch)
	if err != nil {
		return Inventory{}, err
	}
	if r.imageExists(ctx, tag) {
		return r.verify(ctx, u, tag, r.arch)
	}
	candidate, err := r.buildCandidate(ctx, u, r.arch, false)
	if err != nil {
		return Inventory{}, err
	}
	inv, err := r.verify(ctx, u, candidate, r.arch)
	if err == nil {
		_, err = r.command(ctx, nil, "tag", candidate, tag)
	}
	return inv, errors.Join(err, r.untag(ctx, candidate))
}

// Record rebuilds the userland for arch with no build cache, stores the digest of
// the result in the manifest, and tags the image. Writing the manifest back is the
// caller's.
func (r *Runner) Record(ctx context.Context, id ID, arch string) (Inventory, error) {
	u, err := r.manifest.Userland(id)
	if err != nil {
		return Inventory{}, err
	}
	candidate, err := r.buildCandidate(ctx, u, arch, true)
	if err != nil {
		return Inventory{}, err
	}
	inv, err := r.describe(ctx, candidate, arch)
	if err == nil {
		if u.Trees == nil {
			u.Trees = map[string]string{}
		}
		u.Trees[arch] = inv.Tree
		var tag string
		if tag, err = u.Tag(arch); err == nil {
			_, err = r.command(ctx, nil, "tag", candidate, tag)
		}
	}
	return inv, errors.Join(err, r.untag(ctx, candidate))
}

func (r *Runner) verify(ctx context.Context, u *Userland, image, arch string) (Inventory, error) {
	inv, err := r.describe(ctx, image, arch)
	if err != nil {
		return inv, err
	}
	if recorded := u.Trees[arch]; inv.Tree != recorded {
		return inv, fmt.Errorf("userland %s for %s hashes to %s, not the recorded %s: the image did not reproduce from its pins",
			u.ID, arch, inv.Tree, recorded)
	}
	return inv, nil
}

// describe checks the image's platform and takes its inventory.
func (r *Runner) describe(ctx context.Context, image, arch string) (Inventory, error) {
	out, err := r.command(ctx, nil, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image)
	if err != nil {
		return Inventory{}, err
	}
	if platform := strings.TrimSpace(string(out)); platform != "linux/"+arch {
		return Inventory{}, fmt.Errorf("image %s is %s, not linux/%s", image, platform, arch)
	}
	return r.inventory(ctx, image, arch)
}

func (r *Runner) buildCandidate(ctx context.Context, u *Userland, arch string, noCache bool) (string, error) {
	definition, err := u.Definition()
	if err != nil {
		return "", err
	}
	candidate := fmt.Sprintf("%s:%s-%s-candidate", imageRepository, u.ID, arch)
	args := []string{"build", "--platform", "linux/" + arch, "--tag", candidate}
	if noCache {
		args = append(args, "--no-cache")
	}
	for _, arg := range u.BuildArgs(arch) {
		args = append(args, "--build-arg", arg)
	}
	// The definition arrives on standard input with no build context: every input is a pin.
	cmd := exec.CommandContext(ctx, r.runtime, append(args, "-")...)
	cmd.Stdin = bytes.NewReader(definition)
	cmd.Stdout, cmd.Stderr = r.Log, r.Log
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build userland %s for %s: %w", u.ID, arch, err)
	}
	return candidate, nil
}

func (r *Runner) untag(ctx context.Context, image string) error {
	_, err := r.command(context.WithoutCancel(ctx), nil, "image", "rm", image)
	return err
}
