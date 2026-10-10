package workerworkspace

import (
	"reflect"
	"testing"
)

func TestTouchLedgerDropsOnlySettledJobAndReturnsDetachedPaths(t *testing.T) {
	ledger := &TouchLedger{}
	for _, path := range []string{" b.go ", "a.go", "a.go", "../outside", ""} {
		ledger.RecordTouch("job", path)
	}
	ledger.RecordTouch("other", "a.go")
	paths := ledger.Paths(" job ")
	if !reflect.DeepEqual(paths, []string{"a.go", "b.go"}) {
		t.Fatalf("live touched paths=%v", paths)
	}
	paths[0] = "modified"
	if ledger.Paths("job")[0] != "a.go" {
		t.Fatal("reader modified live rewind fence")
	}
	ledger.ClearJob(" job ")
	if len(ledger.Paths("job")) != 0 || !reflect.DeepEqual(ledger.Paths("other"), []string{"a.go"}) {
		t.Fatal("settled job cleanup changed another active job")
	}
}
