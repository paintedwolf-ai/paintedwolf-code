package vocabularytest

import "github.com/lycaon/lycaon/internal/vocabulary"

func DomainIDs(domain, status string) []string {
	var ids []string
	for _, row := range vocabulary.ExportCatalog() {
		if row.Domain == domain && row.Status == status {
			ids = append(ids, row.ID)
		}
	}
	return ids
}
