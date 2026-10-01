package localdata

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestEveryConfigRootFileIsClassified requires a registry entry for every name
// production code joins onto the config root.
func TestEveryConfigRootFileIsClassified(t *testing.T) {
	t.Parallel()
	found := configRootFileLiterals(t, filepath.Join("..", ".."))
	if len(found) == 0 {
		t.Fatal("scan found no config-root files — the anchor stopped matching, so this guard is inert")
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)

	classified := make(map[string]ConfigRootEntry)
	for _, e := range AllClassifiedEntries() {
		classified[e.Name] = e
	}

	for _, name := range names {
		// Joined onto subdirectories of the config root.
		if name == "known_hosts" || name == enginepaths.SourceCatalogDirName {
			continue
		}
		if _, ok := classified[name]; !ok {
			t.Errorf("config-root entry %q (written in %s) is missing from configRootRegistry",
				name, strings.Join(found[name], ", "))
		}
	}
}

func TestConfigRootRegistryIntegrity(t *testing.T) {
	t.Parallel()
	entries := AllClassifiedEntries()
	if len(entries) == 0 {
		t.Fatal("configRootRegistry is empty")
	}

	seen := make(map[string]bool)
	for _, e := range entries {
		if strings.TrimSpace(e.Name) == "" {
			t.Fatal("configRootRegistry contains an entry with an empty name")
		}
		if strings.Contains(e.Name, "/") || strings.Contains(e.Name, "\\") {
			t.Errorf("entry %q contains path separators; configRootRegistry must only classify top-level entries", e.Name)
		}
		if seen[e.Name] {
			t.Errorf("entry %q is registered more than once in configRootRegistry", e.Name)
		}
		seen[e.Name] = true

		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("entry %q has an empty Reason; every entry must declare its rationale", e.Name)
		}

		if e.Class == "" {
			t.Errorf("entry %q has an empty Class", e.Name)
		}

		// Secrets are excluded from backup and diagnostics.
		if e.IsSecret {
			if e.IncludeInBackup {
				t.Errorf("secret entry %q must NOT have IncludeInBackup=true", e.Name)
			}
			if e.IncludeInDiagnostics {
				t.Errorf("secret entry %q must NOT have IncludeInDiagnostics=true", e.Name)
			}
		}

		// Archive-excluded entries are omitted from backup.
		if e.Class == ClassArchiveExcluded && e.IncludeInBackup {
			t.Errorf("archive-excluded entry %q must NOT have IncludeInBackup=true", e.Name)
		}

		if e.Class == ClassLocalDataBucket && e.IncludeInBackup {
			t.Errorf("local data bucket %q must not have IncludeInBackup=true", e.Name)
		}

		lookedUp, ok := LookupConfigRootEntry(e.Name)
		if !ok || lookedUp.Name != e.Name || lookedUp.Class != e.Class {
			t.Errorf("LookupConfigRootEntry(%q) failed or returned mismatched entry", e.Name)
		}
	}
}

// TestDBWipeMatchesStoreCoupled requires scripts/db-wipe.sh to remove exactly
// the store-coupled entries.
func TestDBWipeMatchesStoreCoupled(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "db-wipe.sh"))
	testutil.FailErr(t, "read scripts/db-wipe.sh", err)
	script := string(data)

	dirLoop := regexp.MustCompile(`for name in ([^;]+);\s*do\s*path="\$\{DATA_DIR\}/\$\{name\}"\s*if \[\[ -e "\$\{path\}" \]\];\s*then\s*rm -rf`).FindStringSubmatch(script)
	if dirLoop == nil {
		t.Fatal("could not find directory wipe loop in scripts/db-wipe.sh")
	}
	wipedDirs := strings.Fields(dirLoop[1])
	sort.Strings(wipedDirs)
	if !slices.Equal(wipedDirs, storeCoupledRelDirs) {
		t.Errorf("scripts/db-wipe.sh wiped directories mismatch:\n  got:  %v\n  want: %v", wipedDirs, storeCoupledRelDirs)
	}

	storeLoop := regexp.MustCompile(`for path in ((?:"\$\{DB_PATH\}[^"]*"\s*)+);`).FindStringSubmatch(script)
	if storeLoop == nil {
		t.Fatal("could not find store wipe loop in scripts/db-wipe.sh")
	}
	var wipedFiles []string
	for _, quoted := range strings.Fields(storeLoop[1]) {
		wipedFiles = append(wipedFiles, storeFileName+strings.TrimPrefix(strings.Trim(quoted, `"`), "${DB_PATH}"))
	}

	dbLoop := regexp.MustCompile(`for name in ([^;]+);\s*do\s*for suffix in ([^;]+);`).FindStringSubmatch(script)
	if dbLoop == nil {
		t.Fatal("could not find secondary database wipe loop in scripts/db-wipe.sh")
	}
	for _, name := range strings.Fields(dbLoop[1]) {
		for _, suffix := range strings.Fields(dbLoop[2]) {
			wipedFiles = append(wipedFiles, name+strings.Trim(suffix, `"`))
		}
	}
	sort.Strings(wipedFiles)
	if !slices.Equal(wipedFiles, storeCoupledRelPaths) {
		t.Errorf("scripts/db-wipe.sh wiped files mismatch:\n  got:  %v\n  want: %v", wipedFiles, storeCoupledRelPaths)
	}
}

func TestSecretCanaryExclusion(t *testing.T) {
	t.Parallel()

	backupPaths := BackupRelPaths()
	backupDirs := BackupRelDirs()

	for _, e := range AllClassifiedEntries() {
		if e.IsSecret || e.Class == ClassArchiveExcluded {
			if slices.Contains(backupPaths, e.Name) {
				t.Errorf("secret entry %q found in BackupRelPaths()", e.Name)
			}
			if slices.Contains(backupDirs, e.Name) {
				t.Errorf("secret entry %q found in BackupRelDirs()", e.Name)
			}
		}
		if e.IsSecret && IsDiagnosticsAllowed(e.Name) {
			t.Errorf("secret entry %q is allowed in diagnostics", e.Name)
		}
	}

	if IsDiagnosticsAllowed("unknown_secret.txt") {
		t.Error("unclassified file unknown_secret.txt is allowed in diagnostics")
	}
	if IsDiagnosticsAllowed("arbitrary.yaml") {
		t.Error("unclassified yaml file arbitrary.yaml is allowed in diagnostics")
	}
}

func TestLocalDataBucketsMatchRegistry(t *testing.T) {
	t.Parallel()
	claimed := map[string]bool{}
	for _, e := range AllClassifiedEntries() {
		if e.Bucket != "" && !Known(e.Bucket) {
			t.Errorf("entry %q names unknown bucket %q", e.Name, e.Bucket)
		}
		if e.Class == ClassLocalDataBucket && e.Bucket == "" {
			t.Errorf("local data entry %q names no bucket", e.Name)
		}
		claimed[e.Bucket] = true
	}
	for _, id := range Catalog() {
		if !claimed[id] {
			t.Errorf("bucket %q owns no registry entry", id)
		}
	}
}

// configRootFileLiterals maps a filename to the "file:function" sites that join
// it onto a resolved config root.
func configRootFileLiterals(t *testing.T, moduleRoot string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "node_modules" || name == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		parsed, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(moduleRoot, path)
		if relErr != nil {
			rel = path
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !resolvesConfigRoot(fn.Body) {
				continue
			}
			for _, name := range joinedFilenames(fn.Body) {
				site := filepath.ToSlash(rel) + ":" + fn.Name.Name
				if !slices.Contains(found[name], site) {
					found[name] = append(found[name], site)
				}
			}
		}
		return nil
	})
	testutil.FailErr(t, "walk module", err)
	return found
}

// resolvesConfigRoot reports whether body calls configdir.UserConfigDir.
func resolvesConfigRoot(body *ast.BlockStmt) bool {
	anchored := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "UserConfigDir" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "configdir" {
				anchored = true
			}
		}
		return true
	})
	return anchored
}

// joinedFilenames returns the literal second argument of every two-argument
// filepath.Join in body — the filename placed directly under the joined root.
func joinedFilenames(body *ast.BlockStmt) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Join" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "filepath" {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(lit.Value)
		if err != nil || name == "" || strings.Contains(name, "/") {
			return true
		}
		out = append(out, name)
		return true
	})
	return out
}

// Editor outbox data survives backup and store reset through the shared layout.
func TestNativeEditorOutboxBelongsToDurableInstallation(t *testing.T) {
	t.Parallel()
	var format struct {
		Directory string `json:"directory"`
		Lock      string `json:"lock"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "editoroutbox", "format.json"))
	testutil.FailErr(t, "read native editor layout", err)
	testutil.FailErr(t, "decode native editor layout", json.Unmarshal(raw, &format))
	if format.Directory == "" || !slices.Contains(BackupRelDirs(), format.Directory) || !slices.Contains(StoreResetRecoveryRelDirs(), format.Directory) {
		t.Fatal("native editor work is outside durable backup or reset recovery")
	}
	if strings.HasPrefix(format.Lock, format.Directory+"/") {
		t.Fatal("capture lock moves with replaced editor data")
	}
	native, err := os.ReadFile(filepath.Join("..", "..", "..", "lycaon-den", "src-tauri", "src", "document_outbox.rs"))
	testutil.FailErr(t, "read native editor owner", err)
	if !strings.Contains(strings.Join(strings.Fields(string(native)), ""), `include_str!("../../../lycaon/internal/editoroutbox/format.json")`) {
		t.Fatal("native editor owner does not use the shared durable layout")
	}
}
