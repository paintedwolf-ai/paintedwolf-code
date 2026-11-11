package contract

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
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

func TestNativeToolsReadRejectsDirectoryListing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	contractcheck.FailErr(t, "mkdir", os.Mkdir(filepath.Join(root, "dir"), 0o755))
	_, err := sourceview.ReadContentCapped("read", sourceview.AccessRead, fseffect.Location{Root: root, Rel: "dir"}, "dir", false, 1024)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "READ_IS_DIRECTORY" {
		t.Fatalf("reading a directory = %v, want READ_IS_DIRECTORY", err)
	}
}
