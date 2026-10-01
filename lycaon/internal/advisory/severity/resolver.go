package severity

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Resolver maps an advisory reference to an authoritative CVSS record.
type Resolver interface {
	Resolve(ref *api.AdvisoryRef) (*api.AdvisoryCVSS, string, api.FindingLevel, bool)
}

type indexResolver struct {
	entries map[string]Entry
}

// NewResolver indexes entries by case-insensitive advisory id.
func NewResolver(entries ...Entry) Resolver {
	index := make(map[string]Entry, len(entries))
	for _, e := range entries {
		if key := normalizeID(e.ID); key != "" {
			index[key] = e
		}
	}
	return &indexResolver{entries: index}
}

func normalizeID(id string) string { return strings.ToUpper(strings.TrimSpace(id)) }

func (r *indexResolver) lookup(id string) (Entry, bool) {
	key := normalizeID(id)
	if key == "" {
		return Entry{}, false
	}
	e, ok := r.entries[key]
	return e, ok
}

func (r *indexResolver) Resolve(ref *api.AdvisoryRef) (*api.AdvisoryCVSS, string, api.FindingLevel, bool) {
	if ref == nil {
		return nil, "", api.FindingLevelUnknown, false
	}
	for _, family := range [][]string{ref.CVEIDs, ref.GHSAIDs, append([]string{ref.OSVID}, ref.Aliases...)} {
		for _, id := range family {
			if entry, ok := r.lookup(id); ok {
				return entryCVSS(entry)
			}
		}
	}
	return nil, "", api.FindingLevelUnknown, false
}

func entryCVSS(entry Entry) (*api.AdvisoryCVSS, string, api.FindingLevel, bool) {
	cvss := &api.AdvisoryCVSS{
		Type:   entry.Type,
		Vector: entry.Vector,
		Score:  &entry.Score,
	}
	return cvss, entry.Source, ScoreLevel(entry.Score), true
}
