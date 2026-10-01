package survey

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

type findDistEntry struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func findResultDistribution(results []findResult) []findDistEntry {
	extCounts := map[string]int{}
	dirCounts := map[string]int{}
	for _, r := range results {
		ext := strings.ToLower(filepath.Ext(r.Path))
		if ext == "" {
			ext = "(no ext)"
		}
		extCounts[ext]++
		dirCounts[grepPathBucket(r.Path)]++
	}
	var out []findDistEntry
	for k, n := range dirCounts {
		out = append(out, findDistEntry{Kind: "dir", Key: k, Count: n})
	}
	for k, n := range extCounts {
		out = append(out, findDistEntry{Kind: "ext", Key: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func buildFindZoomedOutResponse(ctx context.Context, resp findResponse, collected []findResult) findResponse {
	out := resp
	out.View = surveyViewDigest
	out.Results = nil
	out.Distribution = findResultDistribution(collected)
	total := len(collected)
	if resp.TotalResults > total {
		total = resp.TotalResults
	}
	out.Total = total
	out.Note = toolkit.AppendNote(out.Note, findSpecificsAffordance(ctx))
	out.Selected = 0
	return out
}
