package extensionstate_test

import (
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Transaction fixtures use unit ids from the shipped catalog.

var (
	stockUnitsOnce sync.Once
	stockUnits     []string
	stockUnitsErr  error
)

func loadStockGuidanceUnits() ([]string, error) {
	stockUnitsOnce.Do(func() {
		packs, err := extpacks.DiscoverStockContent()
		if err != nil {
			stockUnitsErr = err
			return
		}
		for _, pc := range packs {
			for _, u := range pc.Units {
				if strings.HasPrefix(u.ID, "guidance/") {
					stockUnits = append(stockUnits, u.ID)
				}
			}
		}
		sort.Strings(stockUnits)
	})
	return stockUnits, stockUnitsErr
}

func stockUnitIDAt(t *testing.T, i int) string {
	t.Helper()
	units, err := loadStockGuidanceUnits()
	testutil.FailErr(t, "discover stock units", err)
	if len(units) == 0 {
		t.Fatal("shipped catalog has no guidance units")
	}
	return units[i%len(units)]
}
