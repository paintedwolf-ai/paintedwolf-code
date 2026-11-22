package contract

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/hints"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/standingpatterns"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// overlayLoader reads one overlay file of a project the way the product does
// and reports anything it refuses. Where the product falls back silently on a
// file it cannot read, the loader decodes strictly into the type the product
// fills, so a key the current type no longer has is refused.
type overlayLoader func(t *testing.T, projectDir, file string) error

func strictInto[T any](_ *testing.T, _, file string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var target T
	return config.DecodeYAML(data, &target)
}

func overlayLoaders(moduleRoot string) map[string]overlayLoader {
	return map[string]overlayLoader{
		settingsoverlay.FormatFileName: func(_ *testing.T, projectDir, _ string) error {
			return settingsoverlay.CheckFormat(settingsoverlay.Dir(projectDir))
		},
		settingsoverlay.BasenameIgnores: func(_ *testing.T, projectDir, file string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if _, err := projectignore.Parse(data); err != nil {
				return err
			}
			_, defects, err := projectignore.ReadSecrets(projectDir)
			if err == nil && len(defects) > 0 {
				err = fmt.Errorf("secret entries refused: %+v", defects)
			}
			return err
		},
		settingsoverlay.BasenameApprovals: func(t *testing.T, projectDir, _ string) error {
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
			if err != nil {
				return err
			}
			if rejected := store.ProjectOverlay(projectDir).Rejected; len(rejected) > 0 {
				return fmt.Errorf("approval entries refused: %+v", rejected)
			}
			return nil
		},
		settingsoverlay.BasenameLimits: strictInto[settings.SessionLimits],
		settingsoverlay.BasenameVerify: strictInto[settings.VerifyConfig],
		settingsoverlay.BasenameReview: strictInto[settings.ReviewConfig],
		settingsoverlay.BasenameMCP: func(_ *testing.T, _, file string) error {
			_, err := mcp.LoadUserMCPConfig(file)
			return err
		},
		settingsoverlay.BasenameModelPolicy: func(t *testing.T, projectDir, _ string) error {
			store, err := llm.NewPolicyStoreAt(filepath.Join(t.TempDir(), "model-policy.yaml"))
			if err != nil {
				return err
			}
			_, err = store.Get(llm.SettingsScopeProject, projectDir)
			return err
		},
		settingsoverlay.BasenameStandingPatterns: func(t *testing.T, projectDir, _ string) error {
			_, err := standingpatterns.Load(t.Context(), projectDir)
			return err
		},
		settingsoverlay.BasenameHostResources: func(_ *testing.T, _, file string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			_, err = hostresources.ParseCatalog(data, file)
			return err
		},
		settingsoverlay.BasenameExtensions: func(_ *testing.T, _, file string) error {
			_, err := extpacks.LoadDesiredFile(file)
			return err
		},
		settingsoverlay.BasenameExtensionsLock: func(_ *testing.T, _, file string) error {
			_, err := extpacks.LoadLockFile(file)
			return err
		},
		"scanners.yaml": func(t *testing.T, projectDir, _ string) error {
			_, rejected, err := catalog.LoadMergedScannerCatalog(moduleRoot, projectDir, t.TempDir())
			if err == nil && len(rejected) > 0 {
				err = fmt.Errorf("scanner rows refused: %+v", rejected)
			}
			return err
		},
		"scan-hints.yaml": func(_ *testing.T, _, file string) error {
			_, err := hints.Load(file)
			return err
		},
		"detection-packs.yaml": func(t *testing.T, projectDir, _ string) error {
			cat, err := detectionpack.LoadCatalog(detectionpack.Input{ConfigDir: t.TempDir(), ProjectDir: projectDir})
			if err == nil && len(cat.Rejected) > 0 {
				err = fmt.Errorf("detection pack rows refused: %+v", cat.Rejected)
			}
			return err
		},
		"source-scope.yaml": func(_ *testing.T, _, file string) error {
			_, err := sourcescope.LoadConfig(file)
			return err
		},
		settingsoverlay.BasenamePostures: func(_ *testing.T, projectDir, _ string) error {
			base, err := profiles.LoadPostureRegistry()
			if err != nil {
				return err
			}
			_, err = profiles.MergePostureOverlays(base, []string{projectDir})
			return err
		},
	}
}

// overlaySource is one overlay a published release carried: its top-level
// settings files by basename.
type overlaySource struct {
	name  string
	files map[string][]byte
}

// Every overlay setting the product reads has a loader, and the example overlay
// that each new release corpus records holds every one of them.
func TestEveryOverlaySettingHasALoaderAndAnExample(t *testing.T) {
	moduleRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	loaders := overlayLoaders(moduleRoot)
	names := append(settingsoverlay.SettingsOverlayBasenames(), settingsoverlay.ScanConfigBasenames()...)
	names = append(names, settingsoverlay.BasenamePostures, settingsoverlay.FormatFileName)
	slices.Sort(names)
	names = slices.Compact(names)
	example := exampleOverlay(t, moduleRoot)
	for _, name := range names {
		if loaders[name] == nil {
			t.Errorf("overlay setting %s has no loader in this contract", name)
		}
		if _, ok := example.files[name]; !ok {
			t.Errorf("the example overlay has no %s", name)
		}
	}
	for name := range loaders {
		if !slices.Contains(names, name) {
			t.Errorf("loader for %s names no overlay setting", name)
		}
	}
}

// Every overlay a published release carried still loads: the release corpora's
// project overlays, the repository's own overlays at each post-v1 tag, and the
// example overlay the next corpus records.
func TestReleasedOverlaysStillLoad(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	moduleRoot := filepath.Join(root, "lycaon")
	loaders := overlayLoaders(moduleRoot)
	sources := append(corpusOverlays(t, moduleRoot), taggedOverlays(t, root)...)
	sources = append(sources, exampleOverlay(t, moduleRoot))
	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			projectDir := t.TempDir()
			dir := settingsoverlay.Dir(projectDir)
			contractcheck.FailErr(t, "create overlay", os.MkdirAll(dir, 0o755))
			for name, data := range source.files {
				contractcheck.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), data, 0o600))
			}
			for name := range source.files {
				load := loaders[name]
				if load == nil {
					t.Errorf("%s has no loader", name)
					continue
				}
				if err := load(t, projectDir, filepath.Join(dir, name)); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}
		})
	}
}

func readOverlayDir(t *testing.T, name, dir string) overlaySource {
	t.Helper()
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read "+dir, err)
	source := overlaySource{name: name, files: map[string][]byte{}}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		contractcheck.FailErr(t, "read "+entry.Name(), err)
		source.files[entry.Name()] = data
	}
	return source
}

func exampleOverlay(t *testing.T, moduleRoot string) overlaySource {
	t.Helper()
	return readOverlayDir(t, "example", filepath.Join(moduleRoot, "testdata", "overlay-example", "overlay-dir"))
}

func corpusOverlays(t *testing.T, moduleRoot string) []overlaySource {
	t.Helper()
	versions, err := os.ReadDir(filepath.Join(moduleRoot, "testdata", "upgrade-corpus"))
	contractcheck.FailErr(t, "read upgrade corpus", err)
	var out []overlaySource
	for _, version := range versions {
		dir := filepath.Join(moduleRoot, "testdata", "upgrade-corpus", version.Name(), "project-root", "overlay-dir")
		if _, err := os.Stat(dir); err == nil {
			out = append(out, readOverlayDir(t, "corpus-"+version.Name(), dir))
		}
	}
	if len(out) == 0 {
		t.Fatal("no release corpus carries a project overlay")
	}
	return out
}

// taggedOverlays reads the repository's own overlays at every post-v1 tag
// merged into HEAD.
func taggedOverlays(t *testing.T, root string) []overlaySource {
	t.Helper()
	dirs := []string{settingsoverlay.DirName(), path.Join("lycaon", settingsoverlay.DirName())}
	type pendingOverlay struct {
		name  string
		blobs map[string]string
	}
	var pending []pendingOverlay
	seen := map[string]bool{}
	var shas []string
	for _, tag := range postV1Tags(t, root) {
		listing := gitOutput(t, root, "ls-tree", "-r", tag, "--", dirs[0], dirs[1])
		byDir := map[string]map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
			meta, file, ok := strings.Cut(line, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 3 || fields[1] != "blob" || path.Ext(file) != ".yaml" {
				continue
			}
			dir := path.Dir(file)
			if dir != dirs[0] && dir != dirs[1] {
				continue
			}
			if byDir[dir] == nil {
				byDir[dir] = map[string]string{}
			}
			sha := fields[2]
			byDir[dir][path.Base(file)] = sha
			if !seen[sha] {
				seen[sha] = true
				shas = append(shas, sha)
			}
		}
		for _, dir := range dirs {
			if len(byDir[dir]) > 0 {
				pending = append(pending, pendingOverlay{
					name:  tag + "-" + strings.ReplaceAll(dir, "/", "-"),
					blobs: byDir[dir],
				})
			}
		}
	}
	if len(pending) == 0 {
		t.Fatal("no post-v1 tag is merged into HEAD; fetch tags")
	}
	blobs := gitCatFileBatch(t, root, shas)
	out := make([]overlaySource, 0, len(pending))
	for _, item := range pending {
		source := overlaySource{name: item.name, files: make(map[string][]byte, len(item.blobs))}
		for name, sha := range item.blobs {
			source.files[name] = blobs[sha]
		}
		out = append(out, source)
	}
	return out
}
