package anchor

import "github.com/lycaon/lycaon/internal/anchorcatalog"

// DedupPolicy holds catalog-defined suppression for one anchor.
type DedupPolicy struct {
	Key            string
	OmitHintCodes  []string
	OmitInformWhen []string
}

func dedupPolicy(id ID) (DedupPolicy, bool) {
	d, ok := anchorcatalog.DedupFor(string(id))
	if !ok {
		return DedupPolicy{}, false
	}
	return DedupPolicy{
		Key:            d.Key,
		OmitHintCodes:  append([]string(nil), d.OmitHintCodes...),
		OmitInformWhen: append([]string(nil), d.OmitInformWhen...),
	}, true
}
