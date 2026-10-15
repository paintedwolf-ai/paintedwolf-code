package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

const packsRoot = "lycaon/config/packs"

func checkReleaseCompatibility(ctx context.Context, repoRoot, baseline string, out io.Writer) error {
	git := gitRelease{ctx: ctx, repoRoot: repoRoot}
	source, err := git.run("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Release compatibility source: %s\n", bytes.TrimSpace(source))
	if err := checkPackAPIs(repoRoot, out); err != nil {
		return err
	}
	shallow, err := git.run("rev-parse", "--is-shallow-repository")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(shallow)) != "false" {
		return fmt.Errorf("release history is shallow; fetch full history and tags")
	}
	var releases []string
	if baseline != "" {
		releases = []string{baseline}
	} else {
		tags, err := git.run("tag", "--merged", "HEAD", "--list", "v*")
		if err != nil {
			return err
		}
		var latest *semver.Version
		for _, tag := range strings.Fields(string(tags)) {
			version, err := semver.StrictNewVersion(strings.TrimPrefix(tag, "v"))
			if err == nil && version.Major() >= 1 && version.Prerelease() == "" && (latest == nil || version.GreaterThan(latest)) {
				latest, releases = version, []string{tag}
			}
		}
	}
	if len(releases) == 0 {
		return fmt.Errorf("no stable post-v1 release baseline is available")
	}
	for _, tag := range releases {
		git.tag = tag
		version, err := git.show("VERSION")
		if err != nil {
			return err
		}
		released, err := semver.StrictNewVersion(strings.TrimSpace(string(version)))
		if err != nil || released.Major() < 1 || released.Prerelease() != "" {
			return fmt.Errorf("baseline %s is not a stable post-v1 release", tag)
		}
		if baseline == "" && released.String() != strings.TrimPrefix(tag, "v") {
			return fmt.Errorf("%s does not match its VERSION", tag)
		}
		if _, err := git.run("merge-base", "--is-ancestor", tag, "HEAD"); err != nil {
			return fmt.Errorf("stable baseline is not an ancestor of the candidate: %w", err)
		}
		if err := checkMigrationHistory(git, diskRelease{root: repoRoot}); err != nil {
			return err
		}
		beforeAPI, err := sourceExtensionAPI(git)
		if err != nil {
			return err
		}
		afterAPI, err := sourceExtensionAPI(diskRelease{root: repoRoot})
		if err != nil {
			return err
		}
		if afterAPI.LessThan(beforeAPI) {
			return fmt.Errorf("extension API version regressed")
		}
		if err := checkAnchorVersions(git, diskRelease{root: repoRoot}); err != nil {
			return err
		}
		if err := checkAgentVocabulary(git, diskRelease{root: repoRoot}); err != nil {
			return err
		}
		sha, err := git.run("rev-parse", tag+"^{commit}")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Stable baseline: %s %s\n", "v"+released.String(), bytes.TrimSpace(sha))
		files, err := git.run("ls-tree", "-r", "--name-only", "-z", tag, "--", packsRoot+"/")
		if err != nil {
			return err
		}
		releasedPaths := strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00")
		for _, rel := range releasedPaths {
			if strings.HasSuffix(rel, "/meta.yaml") {
				if err := checkReleasedSuite(git, diskRelease{root: repoRoot}, rel); err != nil {
					return err
				}
			}
			if strings.HasSuffix(rel, "/extension.yaml") {
				if err := checkReleasedPack(git, repoRoot, strings.TrimSuffix(rel, "/extension.yaml"), releasedPaths); err != nil {
					return fmt.Errorf("%s: %w", tag, err)
				}
			}
		}
		for _, rel := range releasedPaths {
			pack, workflow, ok := workflowPath(rel)
			if !ok {
				continue
			}
			if err := checkReleasedWorkflow(git, repoRoot, pack, workflow); err != nil {
				return fmt.Errorf("%s %s: %w", tag, rel, err)
			}
			fmt.Fprintf(out, "Preserved workflow: %s/%s\n", pack, workflow)
		}
	}
	return nil
}

func workflowPath(rel string) (string, string, bool) {
	pack, rest, ok := strings.Cut(rel, "/workflows/")
	if !ok || !strings.HasPrefix(pack, packsRoot+"/") || !strings.HasSuffix(rest, "/workflow.yaml") {
		return "", "", false
	}
	workflow := strings.TrimSuffix(rest, "/workflow.yaml")
	if workflow == "" || strings.Contains(workflow, "/") || strings.HasPrefix(workflow, "_") {
		return "", "", false
	}
	return pack, workflow, true
}

func checkReleasedWorkflow(release releaseSource, repoRoot, pack, workflow string) error {
	files, version, err := sealedFiles(release, pack, workflow)
	if err != nil {
		return err
	}
	current := diskRelease{root: repoRoot}
	livePath := path.Join(pack, "workflows", workflow, "workflow.yaml")
	raw, err := current.show(livePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		manifest, err := workflowdef.ParseManifestYAML(raw)
		if err != nil {
			return err
		}
		if manifest.Version == version {
			live, _, err := sealedFiles(current, pack, workflow)
			if err != nil {
				return err
			}
			if !bytes.Equal(live[sumsFile], files[sumsFile]) {
				return fmt.Errorf("workflow %s@%s changed without a version bump", workflow, version)
			}
			return nil
		}
		prior, err := semver.StrictNewVersion(version)
		if err != nil {
			return err
		}
		next, err := semver.StrictNewVersion(manifest.Version)
		if err != nil {
			return err
		}
		if !next.GreaterThan(prior) {
			return fmt.Errorf("workflow %s version %s must advance beyond %s", workflow, manifest.Version, version)
		}
	}
	return verifyArchive(filepath.Join(repoRoot, filepath.FromSlash(pack), "archive", workflow, version), files)
}

type diskRelease struct{ root string }

func (d diskRelease) show(rel string) ([]byte, error) {
	root, err := os.OpenRoot(d.root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(filepath.FromSlash(rel))
}

func (d diskRelease) list(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(d.root, filepath.FromSlash(dir)))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func (g gitRelease) paths(dir string) ([]string, error) {
	raw, err := g.run("ls-tree", "-r", "--name-only", "-z", g.tag, "--", dir+"/")
	if err != nil {
		return nil, err
	}
	return strings.FieldsFunc(string(raw), func(r rune) bool { return r == 0 }), nil
}

func (d diskRelease) paths(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(filepath.Join(d.root, filepath.FromSlash(dir)), func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			rel, err := filepath.Rel(d.root, file)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	return paths, err
}

// Guidance resolves by catalog unit identity, including providers in other packs.
func releasedGuidance(source releaseSource, rel string) ([]byte, error) {
	paths, err := source.paths(packsRoot)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, file := range paths {
		prefix, ok := strings.CutSuffix(file, "/"+rel)
		if ok && strings.Count(strings.TrimPrefix(prefix, packsRoot+"/"), "/") == 1 {
			matches = append(matches, file)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("guidance %s has %d providers", rel, len(matches))
	}
	return source.show(matches[0])
}
