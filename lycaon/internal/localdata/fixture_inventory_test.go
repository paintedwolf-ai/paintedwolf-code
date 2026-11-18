package localdata

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil/localdatatest"
)

func TestConfigRootFixturesCoverClassification(t *testing.T) {
	expected := make(map[string]localdatatest.Entry)
	for _, entry := range localdatatest.Entries() {
		if _, duplicate := expected[entry.Name]; duplicate {
			t.Fatalf("duplicate fixture entry %q", entry.Name)
		}
		expected[entry.Name] = entry
	}
	for _, actual := range configRootRegistry {
		fixture, ok := expected[actual.Name]
		if !ok {
			t.Errorf("config-root path %q has no independent fixture", actual.Name)
			continue
		}
		preserve := actual.Class == ClassDurableFile || actual.Class == ClassDurableDir || actual.Class == ClassArchiveExcluded
		if fixture.IsDir != actual.IsDir || fixture.Preserve != preserve || fixture.IsSecret != actual.IsSecret || fixture.Diagnostics != (actual.IncludeInDiagnostics && !actual.IsSecret) {
			t.Errorf("config-root classification differs from fixture for %q: fixture=%+v actual=%+v", actual.Name, fixture, actual)
		}
		delete(expected, actual.Name)
	}
	for name := range expected {
		t.Errorf("fixture path %q is not classified", name)
	}
}
