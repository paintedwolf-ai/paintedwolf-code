package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/scan/onefile"
)

func relocateConsumer(ctx context.Context, producer *bundled.Manifest, opts options, recipe bundled.ConsumerRecipe) error {
	if recipe.Kind != "nix-nuitka-relocation" || len(recipe.Arguments) != 4 || recipe.Arguments[0] != "--set-interpreter" || recipe.Arguments[2] != "--set-rpath" {
		return fmt.Errorf("Nuitka relocation requires a closed interpreter and RPATH recipe")
	}
	loader := recipe.Arguments[1]
	roots := strings.Split(recipe.Arguments[3], ":")
	for _, path := range append(append([]string{}, roots...), loader) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(path, "/nix/store/") {
			return fmt.Errorf("Nuitka relocation requires explicit Nix store deployment paths")
		}
	}
	if !filepath.IsAbs(opts.root) || runtime.GOOS != "linux" {
		return fmt.Errorf("Nuitka relocation requires an absolute private root on native Linux")
	}
	root, err := filepath.EvalSymlinks(opts.root)
	if err != nil {
		return err
	}
	original, err := filepath.EvalSymlinks(opts.directory)
	if err != nil {
		return err
	}
	flat, err := filepath.EvalSymlinks(bundled.EngineBundledDir(root, producer.OpenGrep.Version))
	if err != nil || flat == original {
		return fmt.Errorf("Nuitka relocation requires a distinct staged producer copy")
	}
	binary, err := bundled.VerifyStagedArtifact(producer, root, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	tool, err := os.Lstat(recipe.ToolPath)
	if err != nil || !tool.Mode().IsRegular() || tool.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("Nuitka relocation requires a regular executable patch tool")
	}
	if err := bundled.VerifyFileSHA256(recipe.ToolPath, recipe.ToolSHA256); err != nil {
		return err
	}
	raw, err := readConsumerInput(binary, onefile.MaxBytes)
	if err != nil {
		return err
	}
	image, err := onefile.Read(raw)
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "paintedwolf-consumer-relocation-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	patcher := consumerELFRelocator{ctx: ctx, recipe: recipe, loader: loader, roots: roots, scratch: scratch, members: make(map[string]onefile.Member)}
	for _, member := range image.Members {
		patcher.members[member.Name] = member
	}
	image.Launcher, err = patcher.relocate("opengrep", image.Launcher)
	if err != nil {
		return err
	}
	for index := range image.Members {
		member := &image.Members[index]
		member.Data, err = patcher.relocate(member.Name, member.Data)
		if err != nil {
			return err
		}
	}
	output, err := os.CreateTemp(scratch, "final-")
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	if err := onefile.Write(output, image); err != nil {
		return err
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := producer.ArtifactForCurrentPlatform()
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, binary)
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{Location: fseffect.Location{Root: root, Rel: relative}, Source: io.LimitReader(output, onefile.MaxBytes+1), Mode: 0o755,
		BeforeCommit: func(_ fseffect.Target, result fseffect.Result) error {
			if result.Bytes > onefile.MaxBytes {
				return fmt.Errorf("relocated scanner exceeds its bound")
			}
			return bundled.VerifyFileSHA256(binary, identity.SHA256)
		}})
	return err
}
