package findings

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type advisoryCVSSKey struct {
	kind      string
	vector    string
	hasScore  bool
	scoreBits uint64
}

func cvssIdentity(c api.AdvisoryCVSS) advisoryCVSSKey {
	key := advisoryCVSSKey{kind: c.Type, vector: c.Vector, hasScore: c.Score != nil}
	if c.Score != nil {
		key.scoreBits = math.Float64bits(*c.Score)
	}
	return key
}

func mergeAdvisoryMetadata(a, b *api.AdvisoryRef) *api.AdvisoryRef {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := *a
	union := func(x, y []string) []string {
		items := append(slices.Clone(x), y...)
		slices.Sort(items)
		return slices.Compact(items)
	}
	out.CVEIDs = union(a.CVEIDs, b.CVEIDs)
	out.GHSAIDs = union(a.GHSAIDs, b.GHSAIDs)
	out.Aliases = union(a.Aliases, b.Aliases)
	if out.Kind == "" {
		out.Kind = b.Kind
	}
	out.FixedVersions = union(a.FixedVersions, b.FixedVersions)
	out.URLs = union(a.URLs, b.URLs)
	seen := map[advisoryCVSSKey]bool{}
	out.CVSS = nil
	for _, c := range append(slices.Clone(a.CVSS), b.CVSS...) {
		key := cvssIdentity(c)
		if !seen[key] {
			out.CVSS = append(out.CVSS, c)
			seen[key] = true
		}
	}
	slices.SortFunc(out.CVSS, func(a, b api.AdvisoryCVSS) int {
		if n := cmp.Compare(a.Type, b.Type); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Vector, b.Vector); n != 0 {
			return n
		}
		x, y := cvssIdentity(a), cvssIdentity(b)
		if x.hasScore != y.hasScore {
			if x.hasScore {
				return 1
			}
			return -1
		}
		return cmp.Compare(x.scoreBits, y.scoreBits)
	})
	if out.SeveritySource == "" {
		out.SeveritySource = b.SeveritySource
	}
	return &out
}

func AdvisoryIDs(advisory *api.AdvisoryRef) []string {
	if advisory == nil {
		return nil
	}
	seen := make(map[string]bool, 4)
	out := make([]string, 0, 4)
	add := func(raw string) {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(advisory.OSVID)
	for _, id := range advisory.CVEIDs {
		add(id)
	}
	for _, id := range advisory.GHSAIDs {
		add(id)
	}
	for _, id := range advisory.Aliases {
		add(id)
	}
	return out
}
