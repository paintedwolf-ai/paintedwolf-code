package extensionstate_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Transaction fixtures use unit ids from the active stock catalog, which a
// test may have staged, so the list is read on each call.
func loadStockGuidanceUnits() ([]string, error) {
	packs, err := extpacks.DiscoverStockContent()
	if err != nil {
		return nil, err
	}
	var units []string
	for _, pc := range packs {
		for _, u := range pc.Units {
			if strings.HasPrefix(u.ID, "guidance/") {
				units = append(units, u.ID)
			}
		}
	}
	sort.Strings(units)
	return units, nil
}

func stockUnitIDAt(t *testing.T, i int) string {
	t.Helper()
	units, err := loadStockGuidanceUnits()
	testutil.FailErr(t, "discover stock units", err)
	if len(units) == 0 {
		t.Fatal("stock catalog has no guidance units")
	}
	return units[i%len(units)]
}
