package evidence

import "sort"

// MergeLedger unions src into dst.
func MergeLedger(dst *Ledger, src Ledger) {
	if dst == nil {
		return
	}
	if dst.Handles == nil {
		dst.Handles = make(map[string]Record)
	}
	if dst.ByPath == nil {
		dst.ByPath = make(map[string][]string)
	}
	if dst.byPathOrder == nil {
		dst.byPathOrder = make(map[string][]string)
	}
	if dst.PathFidelity == nil {
		dst.PathFidelity = make(map[string]string)
	}
	for handle, rec := range src.Handles {
		dst.Handles[handle] = rec
	}
	dst.handleOrder = append(dst.handleOrder, src.handleOrder...)
	for path, handles := range src.ByPath {
		dst.ByPath[path] = append(dst.ByPath[path], handles...)
	}
	for path, handles := range src.byPathOrder {
		dst.byPathOrder[path] = append(dst.byPathOrder[path], handles...)
	}
	for path, tier := range src.PathFidelity {
		dst.PathFidelity[path] = mergePathFidelity(dst.PathFidelity[path], tier)
	}
}

// AssembleLedger builds an in-memory ledger from stored evidence rows.
func AssembleLedger(records []Record) Ledger {
	ev := InitLedger()
	if len(records) == 0 {
		return ev
	}
	chronological := append([]Record(nil), records...)
	sort.SliceStable(chronological, func(i, j int) bool {
		return chronological[i].CustodySeq() < chronological[j].CustodySeq()
	})
	for _, rec := range chronological {
		if rec.Handle == "" || rec.SupersededBy != "" {
			continue
		}
		for _, path := range IndexedPathsForRecord(rec) {
			relPath := NormalizeLedgerPath(path)
			if relPath != "" {
				ev.byPathOrder[relPath] = append(ev.byPathOrder[relPath], rec.Handle)
			}
		}
	}
	sorted := append([]Record(nil), records...)
	sort.Slice(sorted, func(i, j int) bool {
		ki, oi := ParseHandleOrdinal(sorted[i].Handle)
		kj, oj := ParseHandleOrdinal(sorted[j].Handle)
		if ki != kj {
			return ki < kj
		}
		return oi < oj
	})
	for _, rec := range sorted {
		handle := rec.Handle
		if handle == "" {
			continue
		}
		ev.Handles[handle] = rec
		ev.handleOrder = append(ev.handleOrder, handle)
		if rec.SupersededBy == "" {
			indexRecordPaths(&ev, handle, rec)
		}
	}
	return ev
}
