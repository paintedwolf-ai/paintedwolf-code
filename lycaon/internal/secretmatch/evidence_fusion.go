package secretmatch

// propagateNonDisclosable marks matches overlapping protected exact values and
// copies the managed reference onto them for presentation. Retired bytes keep
// no reference: theirs no longer resolves.
func propagateNonDisclosable(hits []Match, check func() error) error {
	for i := range hits {
		for j := range hits {
			if check != nil && j%1024 == 0 {
				if err := check(); err != nil {
					return err
				}
			}
			if !overlaps(hits[i], hits[j]) {
				continue
			}
			if hits[j].NonDisclosable {
				hits[i].NonDisclosable = true
			}
			if hits[i].Reference == "" && !hits[i].Retired && hits[j].Reference != "" {
				hits[i].Reference = hits[j].Reference
			}
		}
	}
	return nil
}

// mergeContainerEvidence preserves binding provenance on overlapping hits.
func mergeContainerEvidence(hits []Match, check func() error) ([]Match, error) {
	for i := range hits {
		if hits[i].Source != SourceShapeRule || hits[i].VarName != "" {
			continue
		}
		for j := range hits {
			if check != nil && j%1024 == 0 {
				if err := check(); err != nil {
					return nil, err
				}
			}
			if hits[j].Source != SourceContainerHarvest {
				continue
			}
			if overlaps(hits[i], hits[j]) {
				hits[i].VarName = hits[j].VarName
				hits[i].Container = hits[j].Container
				break
			}
		}
	}
	return hits, nil
}
