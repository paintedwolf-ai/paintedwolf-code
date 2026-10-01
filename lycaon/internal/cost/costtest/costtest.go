// Package costtest builds SQL-backed cost trackers for tests, so tested
// semantics are the shipped SQLTracker semantics.
package costtest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/testdbfixture"
)

// NewTracker returns a SQLTracker over a fresh on-disk SQLite store that is
// closed with the test.
func NewTracker(t *testing.T, pricer cost.Pricer) *cost.SQLTracker {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	return cost.NewSQLTracker(sqlDB, pricer)
}

// NanoUSD is the recorded estimate for a dollar amount.
func NanoUSD(t *testing.T, usd float64) *int64 {
	t.Helper()
	nano, err := cost.USDToNano(usd)
	if err != nil {
		t.Fatalf("convert %v USD: %v", usd, err)
	}
	return &nano
}
