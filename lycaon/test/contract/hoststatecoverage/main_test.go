package hoststatecoverage_test

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/test/contract/invariants/promptrender"
)

func TestMain(m *testing.M) {
	// Production Readers consult the process's active catalog.
	stock, err := promptrender.LoadStock(context.Background())
	if err != nil {
		println("hoststatecoverage: load the stock catalog:", err.Error())
		os.Exit(1)
	}
	extpacks.SetActive(stock.Catalog)
	os.Exit(m.Run())
}
