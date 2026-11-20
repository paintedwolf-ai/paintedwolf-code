package contract

import (
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
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
