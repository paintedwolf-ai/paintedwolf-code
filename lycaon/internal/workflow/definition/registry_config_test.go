package definition

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadRegistryConfigDefaultAmbient(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	ref, err := LoadRegistryConfig(extpacks.OnDisk(dir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	if ref.ID != "implement" || ref.Version != "1.0.0" {
		t.Fatalf("default_ambient = %+v want implement@1.0.0", ref)
	}
}

func TestLoadManifestsRawSkipsRegistryYAML(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	raw, err := LoadManifestsRaw(dir)
	testutil.FailErr(t, "loadManifestsRaw", err)
	if _, ok := raw["registry@"]; ok {
		t.Fatal("registry.yaml must not load as a workflow manifest")
	}
	stock, _, err := LoadPackManifestsForCatalog(nil)
	testutil.FailErr(t, "loadCatalogManifestsWithSources", err)
	if _, ok := stock[ManifestKey("implement", "1.0.0")]; !ok {
		t.Fatal("implement@1.0.0 missing from stock manifests")
	}
}
