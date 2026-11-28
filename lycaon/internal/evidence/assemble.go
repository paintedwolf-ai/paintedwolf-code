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
	dst.observationOrder = append(dst.observationOrder, src.observationOrder...)
	for path, handles := range src.ByPath {
		dst.ByPath[path] = append(dst.ByPath[path], handles...)
	}
	for path, handles := range src.byPathOrder {
		dst.byPathOrder[path] = append(dst.byPathOrder[path], handles...)
	}
	for path, tier := range src.PathFidelity {
		dst.PathFidelity[path] = mergePathFidelity(dst.PathFidelity[path], tier)
	}
	for handle := range src.unavailableBodies {
		if dst.unavailableBodies == nil {
			dst.unavailableBodies = make(map[string]struct{})
		}
		dst.unavailableBodies[handle] = struct{}{}
	}
}

// AssembleLedger builds an in-memory ledger from stored evidence rows.
func AssembleLedger(records []Record) Ledger {
	return AssembleLedgerWithUnavailable(records, nil)
}

// AssembleLedgerWithUnavailable builds an in-memory ledger from stored evidence
// rows while marking any handles in unavailable as having undecodable bodies.
func AssembleLedgerWithUnavailable(records []Record, unavailable map[string]struct{}) Ledger {
	ev := InitLedger()
	if len(unavailable) > 0 {
		ev.unavailableBodies = make(map[string]struct{}, len(unavailable))
		for h := range unavailable {
			if h != "" {
				ev.unavailableBodies[h] = struct{}{}
			}
		}
	}
	if len(records) == 0 {
		return ev
	}
	chronological := append([]Record(nil), records...)
	sort.SliceStable(chronological, func(i, j int) bool {
		return chronological[i].CustodySeq() < chronological[j].CustodySeq()
	})
	for _, rec := range chronological {
		if rec.Handle == "" {
			continue
		}
		ev.observationOrder = append(ev.observationOrder, rec.Handle)
		if rec.SupersededBy != "" || ev.BodyUnavailable(rec.Handle) {
			continue
		}
		for _, path := range IndexedPathsForRecord(rec) {
			relPath := NormalizeLedgerPath(recordDisplayKey(rec, path))
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
		if rec.SupersededBy == "" && !ev.BodyUnavailable(handle) {
			indexRecordPaths(&ev, handle, rec)
		}
	}
	return ev
}

// BodyUnavailable reports whether handle's persisted body blob failed
// verification when the ledger was loaded.
func (ev Ledger) BodyUnavailable(handle string) bool {
	if len(ev.unavailableBodies) == 0 {
		return false
	}
	_, ok := ev.unavailableBodies[handle]
	return ok
}

// UnavailableBodyHandles returns handles in deterministic order whose persisted
// body blobs could not be decoded.
func (ev Ledger) UnavailableBodyHandles() []string {
	if len(ev.unavailableBodies) == 0 {
		return nil
	}
	var out []string
	for handle := range ev.unavailableBodies {
		if _, exists := ev.Handles[handle]; exists {
			out = append(out, handle)
		}
	}
	sort.Strings(out)
	return out
}
