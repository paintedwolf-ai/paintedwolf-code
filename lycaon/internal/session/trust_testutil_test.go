package session

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

// RegisterProjectContextForTest wires a project and its trust store.
func RegisterProjectContextForTest(t *testing.T, m *Host, projectDir string) string {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	surfaces, err := settings.NewTrustSurfacesStoreAt(
		filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
	testutil.FailErr(t, "trust surfaces store", err)

	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, projectDir)
	testutil.FailErr(t, "CreateWithRoot", err)
	m.SetProjectRegistry(reg)
	m.SetEffectiveCatalogDeps("", extpacks.Active(), surfaces)
	return p.ID
}
