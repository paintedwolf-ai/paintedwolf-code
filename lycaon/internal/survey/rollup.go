package survey

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

type pathBucket struct {
	path  string
	hits  []evidence.Record
	count int
}

func emitWithRollup(label string, priority int, records []evidence.Record, emitCap int, sampled bool) (tagged []taggedRecord, emitted, folded int) {
	if len(records) == 0 {
		return nil, 0, 0
	}
	if emitCap <= 0 || len(records) <= emitCap {
		out := make([]taggedRecord, 0, len(records))
		for _, rec := range records {
			out = append(out, taggedRecord{Priority: priority, Label: label, Record: rec})
		}
		return out, len(out), 0
	}

	byPath := map[string]*pathBucket{}
	order := make([]string, 0)
	for _, rec := range records {
		path := strings.TrimSpace(rec.Path)
		if path == "" {
			path = "(no-path)"
		}
		b, ok := byPath[path]
		if !ok {
			b = &pathBucket{path: path}
			byPath[path] = b
			order = append(order, path)
		}
		b.hits = append(b.hits, rec)
		b.count++
	}

	buckets := make([]pathBucket, 0, len(order))
	for _, p := range order {
		buckets = append(buckets, *byPath[p])
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		if buckets[i].count != buckets[j].count {
			return buckets[i].count > buckets[j].count
		}
		return buckets[i].path < buckets[j].path
	})
	if len(buckets) > emitCap {
		buckets = buckets[:emitCap]
	}

	out := make([]taggedRecord, 0, len(buckets))
	keptHits := 0
	for _, b := range buckets {
		keptHits += b.count
		rec := rollupRecord(label, b, sampled)
		out = append(out, taggedRecord{Priority: priority, Label: label, Record: rec})
	}
	return out, len(out), len(records) - keptHits
}

func rollupRecord(label string, b pathBucket, sampled bool) evidence.Record {
	first := b.hits[0]
	line := 1
	if len(first.LineRanges) > 0 {
		line = first.LineRanges[0].Start
	}
	excerpt := ""
	if len(first.Body) > 0 {
		excerpt = first.Body[0]
		if idx := strings.Index(excerpt, ": "); idx >= 0 {
			excerpt = strings.TrimSpace(excerpt[idx+2:])
		}
	}
	countLabel := "hits"
	if sampled {
		countLabel = "sample_hits"
	}
	body := fmt.Sprintf("[%s] rollup path=%s %s=%d", label, b.path, countLabel, b.count)
	if excerpt != "" {
		body = fmt.Sprintf("%s exemplar: %s", body, excerpt)
	}
	return evidence.Record{
		Kind:       "rollup",
		Shape:      evidence.ShapeFileRegion,
		SourceTool: "survey_probe",
		Survey:     true,
		Path:       first.Path,
		LineRanges: []evidence.LineRange{{Start: line, End: line}},
		Body:       []string{body},
	}
}
