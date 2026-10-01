package catalog

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/testutil"
)

type mutableScannerConfig struct {
	fs.FS
	files fstest.MapFS
}

func TestScannerReadinessUsesTheContractSource(t *testing.T) {
	module := t.TempDir()
	testutil.FailErr(t, "write module marker", os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.invalid/fixture\n"), 0o600))
	gates, err := rules.LoadOpengrepGates()
	testutil.FailErr(t, "load selection", err)
	for _, rel := range rules.ActiveLycaonGatePaths(gates) {
		path := filepath.Join(module, filepath.FromSlash(rel))
		testutil.FailErr(t, "create checkout rule directory", os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "poison unrelated checkout rule", os.WriteFile(path, []byte("invalid: checkout bytes must not be executed\n"), 0o600))
	}
	entry := ScannerEntry{ID: "sast", Driver: DriverBundled, Impl: bundled.ImplOpengrep, Engine: "opengrep", ScopeKind: string(ScopeSourceHostFloor)}
	report := checkEntry(entry, module, t.TempDir())
	if !report.CheckOK {
		t.Fatalf("readiness used checkout bytes instead of the contract source: %+v", report)
	}
}

func (source mutableScannerConfig) Open(name string) (fs.File, error) {
	if _, ok := source.files[name]; ok {
		return source.files.Open(name)
	}
	return source.FS.Open(name)
}

func TestScannerPolicyIdentityTracksMutableSourceWithoutSwap(t *testing.T) {
	gates, err := rules.LoadOpengrepGates()
	testutil.FailErr(t, "load gates", err)
	selected, ok := scannerConfigRel(rules.ActiveLycaonGatePaths(gates)[0])
	if !ok {
		t.Fatal("selected rule has no configuration-relative path")
	}
	for _, rel := range []config.Rel{selected, config.OpengrepGates, config.ScanExcludes} {
		t.Run(string(rel), func(t *testing.T) {
			raw, err := config.Read(rel)
			testutil.FailErr(t, "read policy", err)
			files := fstest.MapFS{string(rel): &fstest.MapFile{Data: raw}}
			t.Cleanup(config.UseFS(mutableScannerConfig{FS: config.Source(), files: files}))
			entry := ScannerEntry{ID: "source-policy", Driver: DriverExternal, Engine: "test", ScopeKind: string(ScopeSourceHostFloor)}
			first := entry.Contract()
			files[string(rel)] = &fstest.MapFile{Data: append(append([]byte(nil), raw...), []byte("\n# changed policy bytes\n")...)}
			second := entry.Contract()
			if first.DefinitionFingerprint == second.DefinitionFingerprint {
				t.Fatal("execution policy identity ignored changed source bytes")
			}
			if third := entry.Contract(); third.DefinitionFingerprint != second.DefinitionFingerprint {
				t.Fatal("unchanged source produced an unstable identity")
			}
		})
	}
}
