package evidence

// RecordAux carries unexported index fields for durable store round-trip.
type RecordAux struct {
	PathsTouched     []string
	URLsTouched      []string
	URLTitles        map[string]string
	GrepLines        map[string]map[int]string
	PathTiers        map[string]string
	UntrustedContent bool
	ExternalCustody  bool
	Seq              int
}

// CloneRecordForStore copies auxiliary index fields for persistence.
func CloneRecordForStore(rec Record) RecordAux {
	cloned := cloneRecord(rec)
	out := RecordAux{
		PathsTouched:     append([]string(nil), cloned.pathsTouched...),
		URLsTouched:      append([]string(nil), cloned.aux.urlsTouched...),
		URLTitles:        cloned.aux.urlTitles,
		UntrustedContent: cloned.UntrustedContent,
		ExternalCustody:  cloned.ExternalCustody,
		Seq:              cloned.aux.seq,
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
	if cloned.aux.pathTiers != nil {
		out.PathTiers = make(map[string]string, len(cloned.aux.pathTiers))
		for path, tier := range cloned.aux.pathTiers {
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
	rec.aux.urlsTouched = append([]string(nil), aux.URLsTouched...)
	rec.aux.urlTitles = nil
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
		rec.aux.pathTiers = make(map[string]string, len(aux.PathTiers))
		for path, tier := range aux.PathTiers {
			rec.aux.pathTiers[NormalizeLedgerPath(path)] = tier
		}
	}
	if aux.UntrustedContent {
		rec.UntrustedContent = true
	}
	if aux.ExternalCustody {
		rec.ExternalCustody = true
	}
	if aux.Seq > 0 {
		rec.aux.seq = aux.Seq
	}
}
