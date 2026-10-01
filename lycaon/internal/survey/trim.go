package survey

import (
	"sort"
	"strconv"

	"github.com/lycaon/lycaon/internal/evidence"
)

type taggedRecord struct {
	Priority int
	Label    string
	Record   evidence.Record
}

func retainCandidates(tagged []taggedRecord, maxCandidates int) []taggedRecord {
	if maxCandidates <= 0 || len(tagged) <= maxCandidates {
		return tagged
	}
	sort.SliceStable(tagged, func(i, j int) bool {
		if tagged[i].Priority != tagged[j].Priority {
			return tagged[i].Priority > tagged[j].Priority
		}
		if tagged[i].Label != tagged[j].Label {
			return tagged[i].Label < tagged[j].Label
		}
		return tagged[i].Record.Handle < tagged[j].Record.Handle
	})
	return tagged[:maxCandidates]
}

func ledgerFromTagged(tagged []taggedRecord) evidence.Ledger {
	records := make([]evidence.Record, len(tagged))
	for i, t := range tagged {
		rec := t.Record
		rec.Handle = fmtHandle(i + 1)
		records[i] = rec
	}
	return evidence.AssembleLedger(records)
}

func fmtHandle(i int) string {
	return "snap#" + strconv.Itoa(i)
}
