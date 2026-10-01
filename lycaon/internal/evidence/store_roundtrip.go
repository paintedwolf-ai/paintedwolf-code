package evidence

// RecordAux carries unexported index fields for durable store round-trip.
type RecordAux struct {
	PathsTouched []string
	URLsTouched  []string
	URLTitles    map[string]string
	GrepLines    map[string]map[int]string
	PathTiers    map[string]string
}

// CloneRecordForStore copies auxiliary index fields for persistence.
func CloneRecordForStore(rec Record) RecordAux {
	cloned := cloneRecord(rec)
	out := RecordAux{
		PathsTouched: append([]string(nil), cloned.pathsTouched...),
		URLsTouched:  append([]string(nil), cloned.urlsTouched...),
		URLTitles:    cloned.urlTitles,
	}
	if cloned.grepLines != nil {
		out.GrepLines = make(map[string]map[int]string, len(cloned.grepLines))
		for path, byLine := range cloned.grepLines {
			lineCopy := make(map[int]string, len(byLine))
			for line, content := range byLine {
				lineCopy[line] = content
			}
			out.GrepLines[path] = lineCopy
		}
	}
	if cloned.pathTiers != nil {
		out.PathTiers = make(map[string]string, len(cloned.pathTiers))
		for path, tier := range cloned.pathTiers {
			out.PathTiers[path] = tier
		}
	}
	return out
}

// ApplyRecordAux restores auxiliary index fields after loading from the store.
func ApplyRecordAux(rec *Record, aux RecordAux) {
	if rec == nil {
		return
	}
	rec.pathsTouched = append([]string(nil), aux.PathsTouched...)
	rec.urlsTouched = append([]string(nil), aux.URLsTouched...)
	rec.urlTitles = nil
	for u, t := range aux.URLTitles {
		rec.touchURLTitle(u, t)
	}
	if aux.GrepLines != nil {
		rec.grepLines = make(map[string]map[int]string, len(aux.GrepLines))
		for path, byLine := range aux.GrepLines {
			path = NormalizeLedgerPath(path)
			rec.grepLines[path] = make(map[int]string, len(byLine))
			for line, content := range byLine {
				rec.grepLines[path][line] = content
			}
		}
	}
	if aux.PathTiers != nil {
		rec.pathTiers = make(map[string]string, len(aux.PathTiers))
		for path, tier := range aux.PathTiers {
			rec.pathTiers[NormalizeLedgerPath(path)] = tier
		}
	}
}
