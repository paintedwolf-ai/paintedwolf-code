//go:build stress

package db

import "testing"

func TestStressDeleteSessionTreeBulkFixture(t *testing.T) {
	assertDeleteSessionTreeFixture(t, benchRunCount, benchChainLen, benchEntryCount, benchChunkRows)
}
