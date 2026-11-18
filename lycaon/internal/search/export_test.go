package search

func EvidenceFTSColumns() []string {
	return append([]string(nil), evidenceFTSColumns...)
}

func EvidenceIndexColumns() []string {
	return append([]string(nil), evidenceIndexColumns...)
}

func EvidenceIndexCompositeIndex() []string {
	return append([]string(nil), evidenceIndexCompositeIndex...)
}

func EvidenceIndexNotNullColumns() []string {
	return append([]string(nil), evidenceIndexNotNullColumns...)
}

func MessagesFTSColumns() []string {
	return append([]string(nil), messagesFTSColumns...)
}
