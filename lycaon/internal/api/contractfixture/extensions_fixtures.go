package contractfixture

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func DecodeMutationView(t *testing.T, body *bytes.Buffer) wire.ExtensionsCatalogView {
	t.Helper()
	var out wire.ExtensionMutationResponse
	testutil.FailErr(t, "decode mutation", json.NewDecoder(body).Decode(&out))
	return out.View
}

// writeAPIMetaSuite writes a community suite with the given member leaves.

func ExtensionRevision(t *testing.T, owner *extensionstate.Owner, projectDir string) string {
	t.Helper()
	revision, err := owner.CurrentRevision(projectDir)
	testutil.FailErr(t, "current extension revision", err)
	return revision
}

func ExtensionsScopeServer(t *testing.T) (*hostapi.Server, project.Registry) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	svc, err := settings.NewService()
	testutil.FailErr(t, "settings.NewService", err)

	reg := project.NewMemoryRegistry()
	deps := hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: store.NewMemory(), Projects: reg, Settings: svc}, Storage: hostapi.StorageDependencies{ModuleRoot: configlayout.FindModuleRoot()}}
	WithExtensionOwner(t)(&deps)
	return hostapi.NewServer(RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken), reg
}

func NewContributionHTTPTestServer(t *testing.T) *hostapi.Server {
	t.Helper()
	srv := NewTestServer(t)
	root := configlayout.FindModuleRoot()
	boot := extpackstest.StockCatalog(t)
	srv.Admin.SessionAdmin.Lifecycle.Sessions.SetEffectiveCatalogDeps(root, boot, nil)
	srv.Admin.SessionAdmin.Lifecycle.Sessions.Catalog.SetCatalogViewCache(catalogview.NewCache(root, slog.Default()))
	return srv
}

func NewExtensionsServer(t *testing.T, opts ...TestDeps) *hostapi.Server {
	t.Helper()
	return NewServerForTest(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: store.NewMemory()}, Storage: hostapi.StorageDependencies{ModuleRoot: configlayout.FindModuleRoot()}}, append([]TestDeps{WithExtensionOwner(t)}, opts...)...)
}

func WithExtensionOwner(t *testing.T) TestDeps {
	t.Helper()
	views := extstatetest.Owner(t).Views
	return func(d *hostapi.Dependencies) { d.Extensions.ExtensionViews = views }
}

// newExtensionsServer serves the module's extension catalog through an owner.

func WriteAPIMetaLeaf(t *testing.T, suite, leaf, id string) {
	t.Helper()
	dir := filepath.Join(suite, leaf)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "guidance"), 0o750))
	testutil.FailErr(t, "manifest", os.WriteFile(filepath.Join(dir, "extension.yaml"), []byte(
		"manifest_version: 1\nid: "+id+"\nname: "+leaf+"\nversion: 1.0.0\ncompatibility:\n  extension_api: \"^1.0.0\"\n",
	), 0o600))
	testutil.FailErr(t, "guidance", os.WriteFile(filepath.Join(dir, "guidance", leaf+".md"), []byte("# "+leaf+"\n"), 0o600))
}

func WriteAPIMetaSuite(t *testing.T, suite, metaID string, leaves ...string) {
	t.Helper()
	members := ""
	for _, leaf := range leaves {
		WriteAPIMetaLeaf(t, suite, leaf, "acme/api-"+leaf)
		members += "  - acme/api-" + leaf + "\n"
	}
	testutil.FailErr(t, "meta.yaml", os.WriteFile(filepath.Join(suite, "meta.yaml"), []byte(
		"manifest_version: 1\nid: "+metaID+"\nname: Kit\nversion: \"1.0.0\"\n"+
			"compatibility:\n  extension_api: \"^1.0.0\"\nmembers:\n"+members+"conflicts_with: []\nextends: []\n",
	), 0o600))
}
