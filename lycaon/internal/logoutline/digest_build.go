package logoutline

import (
	"bytes"
	"sort"
	"strings"
	"time"
)

const (
	digestMaxRecords     = 10000
	digestMaxBytes       = 4 << 20
	digestMaxFields      = 20
	digestMaxFacets      = 8
	digestMaxFacetValues = 10
	digestMaxClusters    = 25
	facetDistinctCap     = 20
)

var (
	facetKeyWhitelist = map[string]bool{
		"severity": true,
		"status":   true,
		"host":     true,
		"method":   true,
		"app":      true,
		"tag":      true,
		"ident":    true,
	}
	facetExcludedKeys = map[string]bool{
		"msg": true, "message": true, "request": true, "request_id": true,
		"user_agent": true, "referer": true, "bytes": true, "name": true,
	}
)

type digestAccumulator struct {
	format      LogFormat
	recordCount int
	parsedCount int
	truncated   bool
	minTime     time.Time
	maxTime     time.Time
	hasTime     bool
	fieldHits   map[string]int
	facetCounts map[string]map[string]int
	clusters    map[string]*clusterAcc
}

type clusterAcc struct {
	count     int
	severity  map[string]int
	firstLine int
	lastLine  int
}

type scannedLine struct {
	num  int
	body []byte
}

// BuildDigest parses content with the parser for format and folds records into
// a Digest.
func BuildDigest(format LogFormat, content []byte) (*Digest, error) {
	if format == "" || format == FormatNone {
		return nil, ErrNotLog
	}
	parser, ok := ParserFor(format)
	if !ok {
		return nil, ErrNotLog
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, ErrNotLog
	}

	lines, totalNonBlank, truncated := scanContentLines(content)
	if totalNonBlank == 0 {
		return nil, ErrNotLog
	}

	acc := &digestAccumulator{
		format:      format,
		recordCount: len(lines),
		truncated:   truncated,
		fieldHits:   map[string]int{},
		facetCounts: map[string]map[string]int{},
		clusters:    map[string]*clusterAcc{},
	}

	for _, line := range lines {
		rec, ok := parser.Parse(line.body)
		if !ok {
			continue
		}
		acc.parsedCount++
		rec.Line = line.num
		acc.observeRecord(rec)
	}

	if acc.parsedCount == 0 {
		return nil, ErrNotLog
	}

	return acc.buildDigest(), nil
}

func scanContentLines(content []byte) ([]scannedLine, int, bool) {
	truncatedByBytes := len(content) > digestMaxBytes
	if truncatedByBytes {
		content = content[:digestMaxBytes]
	}

	var lines []scannedLine
	totalNonBlank := 0
	lineNum := 0
	for _, chunk := range bytes.Split(content, []byte("\n")) {
		lineNum++
		trimmed := bytes.TrimSpace(chunk)
		if len(trimmed) == 0 {
			continue
		}
		totalNonBlank++
		if len(lines) < digestMaxRecords {
			lines = append(lines, scannedLine{num: lineNum, body: trimmed})
		}
	}
	truncated := truncatedByBytes || totalNonBlank > digestMaxRecords
	return lines, totalNonBlank, truncated
}

func (a *digestAccumulator) observeRecord(rec Record) {
	if !rec.Time.IsZero() {
		if !a.hasTime || rec.Time.Before(a.minTime) {
			a.minTime = rec.Time
		}
		if !a.hasTime || rec.Time.After(a.maxTime) {
			a.maxTime = rec.Time
		}
		a.hasTime = true
	}

	for k := range rec.Fields {
		a.fieldHits[k]++
	}

	a.observeFacet("severity", rec.Severity)
	for k, v := range rec.Fields {
		if k == "request" {
			if method := extractHTTPMethod(v); method != "" {
				a.observeFacet("method", method)
			}
			continue
		}
		if v == "" || facetExcludedKeys[k] {
			continue
		}
		a.observeFacet(k, v)
	}

	msg := rec.Message
	if msg == "" {
		msg = rec.Fields["msg"]
	}
	if msg == "" {
		msg = rec.Fields["message"]
	}
	if msg == "" {
		return
	}
	tmpl := maskMessage(msg)
	if tmpl == "" {
		return
	}
	c, ok := a.clusters[tmpl]
	if !ok {
		c = &clusterAcc{
			severity:  map[string]int{},
			firstLine: rec.Line,
			lastLine:  rec.Line,
		}
		a.clusters[tmpl] = c
	}
	c.count++
	if rec.Line < c.firstLine {
		c.firstLine = rec.Line
	}
	if rec.Line > c.lastLine {
		c.lastLine = rec.Line
	}
	if rec.Severity != "" {
		c.severity[rec.Severity]++
	}
}

func (a *digestAccumulator) observeFacet(key, value string) {
	if value == "" || !facetKeyWhitelist[key] {
		return
	}
	if a.facetCounts[key] == nil {
		a.facetCounts[key] = map[string]int{}
	}
	a.facetCounts[key][value]++
}

func extractHTTPMethod(request string) string {
	request = strings.TrimSpace(request)
	if request == "" {
		return ""
	}
	if sp := strings.IndexByte(request, ' '); sp > 0 {
		return request[:sp]
	}
	return ""
}

func (a *digestAccumulator) buildDigest() *Digest {
	d := &Digest{
		Format:      a.format,
		RecordCount: a.recordCount,
		ParsedCount: a.parsedCount,
		Truncated:   a.truncated,
	}
	if a.hasTime {
		d.TimeSpan = &TimeSpan{
			Start: a.minTime.UTC().Format(time.RFC3339),
			End:   a.maxTime.UTC().Format(time.RFC3339),
		}
	}
	d.Fields = buildFieldStats(a.fieldHits, a.parsedCount)
	d.Facets = buildFacets(a.facetCounts, a.parsedCount)
	d.Clusters = buildClusters(a.clusters)
	return d
}

func buildFieldStats(hits map[string]int, parsed int) []FieldStat {
	if parsed == 0 || len(hits) == 0 {
		return nil
	}
	stats := make([]FieldStat, 0, len(hits))
	for k, n := range hits {
		stats = append(stats, FieldStat{
			Key:      k,
			Coverage: coveragePct(n, parsed),
		})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Coverage != stats[j].Coverage {
			return stats[i].Coverage > stats[j].Coverage
		}
		return stats[i].Key < stats[j].Key
	})
	if len(stats) > digestMaxFields {
		stats = stats[:digestMaxFields]
	}
	return stats
}

func buildFacets(counts map[string]map[string]int, parsed int) []Facet {
	if parsed == 0 || len(counts) == 0 {
		return nil
	}
	keys := make([]string, 0, len(counts))
	for k, vals := range counts {
		if len(vals) > facetDistinctCap {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > digestMaxFacets {
		keys = keys[:digestMaxFacets]
	}
	facets := make([]Facet, 0, len(keys))
	for _, k := range keys {
		vals := counts[k]
		values := make([]FacetValue, 0, len(vals))
		for v, n := range vals {
			values = append(values, FacetValue{Value: v, Count: n})
		}
		sort.Slice(values, func(i, j int) bool {
			if values[i].Count != values[j].Count {
				return values[i].Count > values[j].Count
			}
			return values[i].Value < values[j].Value
		})
		if len(values) > digestMaxFacetValues {
			values = values[:digestMaxFacetValues]
		}
		facets = append(facets, Facet{Key: k, Values: values})
	}
	return facets
}

func buildClusters(clusters map[string]*clusterAcc) []Cluster {
	if len(clusters) == 0 {
		return nil
	}
	out := make([]Cluster, 0, len(clusters))
	for tmpl, c := range clusters {
		out = append(out, Cluster{
			Template:  tmpl,
			Count:     c.count,
			Severity:  modalSeverity(c.severity),
			FirstLine: c.firstLine,
			LastLine:  c.lastLine,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].FirstLine < out[j].FirstLine
	})
	if len(out) > digestMaxClusters {
		out = out[:digestMaxClusters]
	}
	return out
}

func modalSeverity(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	type pair struct {
		sev   string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for s, n := range counts {
		pairs = append(pairs, pair{s, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].sev < pairs[j].sev
	})
	return pairs[0].sev
}

func coveragePct(hits, total int) int {
	if total == 0 {
		return 0
	}
	return (hits * 100) / total
}
