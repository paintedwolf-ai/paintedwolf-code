package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Guards: stock scan-guidance pack + no pack-shipped engines.
func TestScannersPackGuards(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	packsRoot := filepath.Join(root, "lycaon", "config", "packs")
	pwRoot := filepath.Join(packsRoot, "painted-wolf")

	t.Run("stock_no_requires_scanners", func(t *testing.T) {
		t.Parallel()
		err := filepath.Walk(pwRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || info.Name() != "extension.yaml" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), "requires_scanners:") {
				rel, _ := filepath.Rel(pwRoot, path)
				t.Errorf("stock pack must not ship requires_scanners: %s", rel)
			}
			return nil
		})
		testutil.FailErr(t, "walk packs", err)
	})

	t.Run("no_pack_scanners_engines_dir", func(t *testing.T) {
		t.Parallel()
		err := filepath.Walk(packsRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(packsRoot, path)
			rel = filepath.ToSlash(rel)
			if strings.HasSuffix(rel, "/scanners/engines") || strings.Contains(rel, "/scanners/engines/") {
				t.Errorf("forbidden pack scanners/engines path: %s", rel)
			}
			return nil
		})
		testutil.FailErr(t, "walk packs", err)
	})

	t.Run("no_pack_root_scanners_yaml", func(t *testing.T) {
		t.Parallel()
		err := filepath.Walk(packsRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(packsRoot, path)
			rel = filepath.ToSlash(rel)
			// pack-root scanners.yaml: <suite>/<leaf>/scanners.yaml
			parts := strings.Split(rel, "/")
			if len(parts) == 3 && parts[2] == "scanners.yaml" {
				t.Errorf("forbidden pack-root scanners.yaml: %s", rel)
			}
			return nil
		})
		testutil.FailErr(t, "walk packs", err)
	})

	t.Run("discover_stock_scan_guidance", func(t *testing.T) {
		t.Parallel()
		packs, err := extpacks.DiscoverStock()
		testutil.FailErr(t, "DiscoverStock", err)
		foundGuidance := false
		for _, p := range packs {
			if p.ID == "painted-wolf/scanners" {
				t.Fatal("painted-wolf/scanners must be absent after rename")
			}
			if p.ID == "painted-wolf/scan-guidance" {
				foundGuidance = true
			}
		}
		if !foundGuidance {
			t.Fatal("painted-wolf/scan-guidance must be discovered")
		}
	})

	t.Run("runtime_stock_catalog_exists", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(root, "lycaon", "config", "runtime", "scanners", "scanners.yaml")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("runtime stock catalog missing: %v", err)
		}
	})

	t.Run("runtime_engine_assets_exist", func(t *testing.T) {
		t.Parallel()
		base := filepath.Join(root, "lycaon", "config", "runtime", "scanners")
		for _, rel := range []string{
			"bundled-manifest.yaml",
			"opengrep-gates.yaml",
			"rules-provenance.yaml",
			filepath.Join("rules", "lycaon"),
			filepath.Join("rules", "vendor"),
		} {
			if _, err := os.Stat(filepath.Join(base, rel)); err != nil {
				t.Errorf("stock engine asset missing: %s: %v", rel, err)
			}
		}
	})

	t.Run("scripts_reference_live_config_paths", func(t *testing.T) {
		t.Parallel()
		assertScriptConfigPathsResolve(t, root)
	})

	t.Run("go_reference_live_scanner_asset_paths", func(t *testing.T) {
		t.Parallel()
		assertGoScannerAssetPathsResolve(t, root)
	})
}

// configPathInScript matches repo-source config paths embedded in shell scripts.
// Anchoring on runtime/ | packs/ | scanners/ keeps staging destinations such as
// "${ENGINE_ROOT}/config/" out of the match set.
var configPathInScript = regexp.MustCompile(`config/(?:runtime|packs|scanners)/[A-Za-z0-9._/-]+`)

// scannerAssetInGo matches a scanner asset filename quoted in Go source, whether it
// arrives as a slash path, a filepath.Join element, or a relative path in a data
// table joined onto a runtime root at call time.
var scannerAssetInGo = regexp.MustCompile(`"((?:[A-Za-z0-9._/-]*/)?(?:bundled-manifest|opengrep-gates|gates|runner|scan-hints|suppressions|rules-provenance|scanners)\.yaml)"`)

// assertGoScannerAssetPathsResolve fails when Go source names a scanner asset that
// does not exist under config.ScannersDir. Bare filenames and paths already
// rooted at config/ are both normalised to that directory before the check, so a
// wrong intermediate segment is caught even when the full path is only assembled
// at runtime.
func assertGoScannerAssetPathsResolve(t *testing.T, root string) {
	t.Helper()
	scannersDir := filepath.Join(root, "lycaon", config.ConfigDirName, filepath.FromSlash(config.ScannersDir.String()))
	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Vendored rule corpora carry their own YAML; they are data, not references.
			if d.Name() == "rules" || d.Name() == "testdata" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range scannerAssetInGo.FindAllStringSubmatch(string(raw), -1) {
			ref := filepath.ToSlash(m[1])
			// Only judge references that point into the scanners tree; an unrelated
			// gates.yaml elsewhere in config/ is not this guard's business.
			trimmed := ref
			if i := strings.Index(ref, "scanners/"); i >= 0 {
				trimmed = ref[i+len("scanners/"):]
			} else if strings.Contains(ref, "/") {
				continue
			}
			if _, err := os.Stat(filepath.Join(scannersDir, filepath.FromSlash(trimmed))); err != nil {
				t.Errorf("%s references missing scanner asset: %s (resolved %s)", rel, ref, trimmed)
			}
		}
		return nil
	})
	testutil.FailErr(t, "walk go sources", err)
}

// assertScriptConfigPathsResolve fails when a shell script names a config path
// that does not exist. A moved config dir rots every script naming it, and only
// the one wired into den:bundle fails loudly on its own.
func assertScriptConfigPathsResolve(t *testing.T, root string) {
	t.Helper()
	scripts, err := filepath.Glob(filepath.Join(root, "scripts", "*.sh"))
	testutil.FailErr(t, "glob scripts", err)
	if len(scripts) == 0 {
		t.Fatal("no scripts/*.sh found")
	}
	for _, script := range scripts {
		raw, err := os.ReadFile(script)
		testutil.FailErr(t, "read script", err)
		name := filepath.Base(script)
		for _, match := range configPathInScript.FindAllString(string(raw), -1) {
			rel := strings.TrimSuffix(match, "/")
			if _, err := os.Stat(filepath.Join(root, "lycaon", rel)); err != nil {
				t.Errorf("%s references missing config path: lycaon/%s", name, rel)
			}
		}
	}
}
