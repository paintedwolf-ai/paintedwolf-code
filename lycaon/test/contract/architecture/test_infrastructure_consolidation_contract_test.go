package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestContractCorpusLoadersRejectEmptySelection(t *testing.T) {
	err := contractcheck.WalkFiles(t.TempDir(), map[string]struct{}{`.go`: {}}, false, func(string, []byte) error {
		return nil
	})
	if err == nil {
		t.Fatal("empty source corpus selection was accepted")
	}
	if _, err := contractcheck.LoadGoASTCorpus(t.TempDir()); err == nil {
		t.Fatal("empty Go corpus selection was accepted")
	}
}
