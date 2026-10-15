package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/internal/extpacks"
)

func checkReleasedPack(release releaseSource, repoRoot, pack string, releasedPaths []string) error {
	rel := pack + "/extension.yaml"
	old, err := release.show(rel)
	if err != nil {
		return err
	}
	prior, err := extpacks.ParseManifest(rel, old)
	if err != nil {
		return err
	}
	raw, err := (diskRelease{root: repoRoot}).show(rel)
	if err != nil {
		return fmt.Errorf("released pack %s is unavailable: %w", prior.ID, err)
	}
	current, err := extpacks.ParseManifest(rel, raw)
	if err != nil {
		return err
	}
	if current.ID != prior.ID {
		return fmt.Errorf("released pack %s changed identity to %s", prior.ID, current.ID)
	}
	before, err := semver.StrictNewVersion(prior.Version)
	if err != nil {
		return err
	}
	after, err := semver.StrictNewVersion(current.Version)
	if err != nil {
		return err
	}
	if after.LessThan(before) {
		return fmt.Errorf("pack %s version %s is older than released %s", current.ID, current.Version, prior.Version)
	}
	for _, file := range releasedPaths {
		if !strings.HasPrefix(file, pack+"/archive/") {
			continue
		}
		old, err := release.show(file)
		if err != nil {
			return err
		}
		current, err := (diskRelease{root: repoRoot}).show(file)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, current) {
			return fmt.Errorf("released archive changed: %s", file)
		}
	}
	if after.GreaterThan(before) {
		return nil
	}
	files := map[string]bool{}
	root := filepath.Join(repoRoot, filepath.FromSlash(pack))
	if err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			rel, err := filepath.Rel(repoRoot, file)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = true
		}
		return nil
	}); err != nil {
		return err
	}
	for _, file := range releasedPaths {
		if !strings.HasPrefix(file, pack+"/") {
			continue
		}
		priorBytes, err := release.show(file)
		if err != nil {
			return err
		}
		currentBytes, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(file)))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err != nil || !bytes.Equal(currentBytes, priorBytes) {
			return fmt.Errorf("pack %s@%s changed without a version bump: %s", current.ID, current.Version, file)
		}
		delete(files, file)
	}
	if len(files) != 0 {
		return fmt.Errorf("pack %s@%s added files without a version bump", current.ID, current.Version)
	}
	return nil
}
