package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/internal/extpacks"
	"gopkg.in/yaml.v3"
)

func archiveVersionPrefix(pack, file string) (string, bool) {
	rest, ok := strings.CutPrefix(file, pack+"/archive/")
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return pack + "/archive/" + parts[0] + "/" + parts[1] + "/", true
}

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
	candidate := diskRelease{root: repoRoot}
	raw, err := candidate.show(rel)
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
	currentPaths, err := candidate.paths(pack)
	if err != nil {
		return err
	}
	releasedArchiveFiles := map[string]bool{}
	releasedArchiveDirs := map[string]bool{}
	for _, file := range releasedPaths {
		if !strings.HasPrefix(file, pack+"/archive/") {
			continue
		}
		releasedArchiveFiles[file] = true
		if prefix, ok := archiveVersionPrefix(pack, file); ok {
			releasedArchiveDirs[prefix] = true
		}
		old, err := release.show(file)
		if err != nil {
			return err
		}
		now, err := candidate.show(file)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, now) {
			return fmt.Errorf("released archive changed: %s", file)
		}
	}
	for _, file := range currentPaths {
		if prefix, ok := archiveVersionPrefix(pack, file); ok && releasedArchiveDirs[prefix] && !releasedArchiveFiles[file] {
			return fmt.Errorf("released archive changed: %s", file)
		}
	}
	if err := checkPackFactFloor(release, candidate, pack, current, before, after, currentPaths); err != nil {
		return err
	}
	if after.GreaterThan(before) {
		return nil
	}
	files := map[string]bool{}
	for _, file := range currentPaths {
		files[file] = true
	}
	for _, file := range releasedPaths {
		if !strings.HasPrefix(file, pack+"/") {
			continue
		}
		priorBytes, err := release.show(file)
		if err != nil {
			return err
		}
		currentBytes, err := candidate.show(file)
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

func checkPackFactFloor(release, current releaseSource, pack string, manifest extpacks.Manifest, before, after *semver.Version, currentPaths []string) error {
	var policyFiles []string
	for _, file := range currentPaths {
		if strings.HasPrefix(file, pack+"/policy/") && strings.HasSuffix(file, ".yaml") {
			policyFiles = append(policyFiles, file)
		}
	}
	if len(policyFiles) == 0 {
		return nil
	}
	beforeFacts, err := factVocabulary(release)
	if err != nil {
		return err
	}
	var beforeAPI, beforePlatform *semver.Version
	for _, file := range policyFiles {
		raw, err := current.show(file)
		if err != nil {
			return err
		}
		var rule struct {
			Requires struct {
				Facts []string `yaml:"facts"`
			} `yaml:"requires"`
		}
		if err := yaml.Unmarshal(raw, &rule); err != nil {
			return err
		}
		for _, fact := range rule.Requires.Facts {
			if _, ok := beforeFacts[fact]; ok {
				continue
			}
			if after.Major() == before.Major() && after.Minor() == before.Minor() {
				return fmt.Errorf("pack %s@%s requires new host fact %s without a minor version bump", manifest.ID, manifest.Version, fact)
			}
			if beforeAPI == nil {
				if beforeAPI, err = sourceExtensionAPI(release); err != nil {
					return err
				}
			}
			apiConstraint, err := semver.NewConstraint(manifest.Compatibility.ExtensionAPI)
			if err != nil {
				return err
			}
			if apiConstraint.Check(beforeAPI) {
				return fmt.Errorf("pack %s requires new host fact %s; extension_api %s admits baseline API %s", manifest.ID, fact, manifest.Compatibility.ExtensionAPI, beforeAPI)
			}
			if manifest.ID == extpacks.PlatformPackID {
				continue
			}
			if beforePlatform == nil {
				platformPath := packsRoot + "/painted-wolf/platform/extension.yaml"
				platformRaw, err := release.show(platformPath)
				if err != nil {
					return err
				}
				platformManifest, err := extpacks.ParseManifest(platformPath, platformRaw)
				if err != nil {
					return err
				}
				if beforePlatform, err = semver.StrictNewVersion(platformManifest.Version); err != nil {
					return err
				}
			}
			dep, ok := manifest.Dependencies[extpacks.PlatformPackID]
			if !ok {
				return fmt.Errorf("pack %s requires new host fact %s without a %s dependency", manifest.ID, fact, extpacks.PlatformPackID)
			}
			depConstraint, err := semver.NewConstraint(dep.Version)
			if err != nil {
				return err
			}
			if depConstraint.Check(beforePlatform) {
				return fmt.Errorf("pack %s requires new host fact %s; %s dependency %s admits baseline %s", manifest.ID, fact, extpacks.PlatformPackID, dep.Version, beforePlatform)
			}
		}
	}
	return nil
}
