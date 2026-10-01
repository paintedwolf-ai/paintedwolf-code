package prompts

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHintCodeCacheIsBoundToRevisionAndContent(t *testing.T) {
	ResetPersonaContractCache()
	t.Cleanup(ResetPersonaContractCache)
	entry := func(instead string) []hintregistry.Entry {
		return []hintregistry.Entry{{
			Code: "SAME_CODE",
			Body: []byte("emit: preventive\ncategory: retry\ninstead: " + instead + "\n"),
		}}
	}
	first, err := loadHintCodeRowsCached("revision-1", entry("first"))
	testutil.FailErr(t, "load first policy", err)
	second, err := loadHintCodeRowsCached("revision-1", entry("second"))
	testutil.FailErr(t, "load changed policy", err)
	if first["SAME_CODE"].Instead != "first" || second["SAME_CODE"].Instead != "second" {
		t.Fatalf("cache crossed policy bytes: first=%q second=%q", first["SAME_CODE"].Instead, second["SAME_CODE"].Instead)
	}
}

func TestLoadAgentToolSurfaceRequiresCatalogBoundProfiles(t *testing.T) {
	if _, err := LoadAgentToolSurface("implement", nil, nil, SurfaceTurn{}, nil); err == nil {
		t.Fatal("surface fell back to process-global profiles")
	}
}

func TestToolSchemaCacheRequiresCatalogRevision(t *testing.T) {
	if _, err := loadToolSchemaCached(&extpacks.EffectiveCatalog{}); err == nil {
		t.Fatal("schema cache accepted a catalog without an identity")
	}
}
