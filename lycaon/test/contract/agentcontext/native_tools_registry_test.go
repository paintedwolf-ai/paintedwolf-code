package contract

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func loadNativeToolsManifestFlat(t *testing.T) []string {
	t.Helper()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "nativemanifest.Load", err)
	out := cfg.AllTools()
	sort.Strings(out)
	return out
}

func TestNativeToolsManifestMatchesServeRegistry(t *testing.T) {
	manifest := loadNativeToolsManifestFlat(t)
	reg := toolfixture.ContractServeBootRegistry(t)
	registered := toolfixture.BootRegisteredToolSet(t, reg)
	for _, tool := range manifest {
		if !registered[tool] {
			t.Fatalf("native-tools.yaml lists %q but serve registry has no handler", tool)
		}
	}
}

func TestNativeToolsManifestHasToolSchemas(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir", err)
	for _, tool := range loadNativeToolsManifestFlat(t) {
		if _, ok := schemas.Tools[tool]; !ok {
			t.Fatalf("tools/schemas missing entry for native tool %q", tool)
		}
	}
}
