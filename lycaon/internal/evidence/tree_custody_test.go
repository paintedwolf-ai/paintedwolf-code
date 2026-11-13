package evidence

import (
	"slices"
	"testing"
)

// A directory a network read filled holds custody for itself and every file
// below it. A later whole-file record on one file decides that file; a later
// tree decides over an earlier file record. Nothing outside the tree is held.
func TestTreeCustodyCoversItsFilesUntilALaterWholeFileRecord(t *testing.T) {
	const origin = "https://git.example.test/org/repo.git"
	tree := Record{Handle: "http_response#1", Kind: "http_response", Shape: ShapeSurfaceSnapshot, SourceTool: "command", Path: "repo", Custody: FileCustody{External: true, Tree: true}}
	tree.touchURL(origin)
	tree.SetCustodySeq(5)
	localBefore := Record{Handle: "write#1", Kind: "write", Shape: ShapeStructuredEvent, SourceTool: "write", Path: "repo/notes.md"}
	localBefore.SetCustodySeq(3)
	localAfter := Record{Handle: "write#2", Kind: "write", Shape: ShapeStructuredEvent, SourceTool: "write", Path: "repo/README.md"}
	localAfter.SetCustodySeq(7)
	ledger := AssembleLedger([]Record{localBefore, tree, localAfter})

	for path, held := range map[string]bool{
		"repo":                 true,
		"repo/src/main.go":     true,
		"repo/notes.md":        true,
		"repo/README.md":       false,
		"other/file.go":        false,
		"repository/inside.go": false,
	} {
		urls := ExternalCustodyURLs(ledger, []string{path})
		if got := slices.Equal(urls, []string{origin}); got != held {
			t.Errorf("%s: custody %v, want held=%v", path, urls, held)
		}
	}

	decoded := Record{}
	ApplyRecordAux(&decoded, CloneRecordForStore(tree))
	if !decoded.Custody.Tree || !decoded.Custody.External || decoded.CustodySeq() != 5 {
		t.Fatalf("tree custody lost in the store round trip: %+v", decoded.Custody)
	}
}
