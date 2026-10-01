package toolusage

import (
	"math"
	"sort"
)

const DefaultRuns = 1

// AggregateProfiles combines repeated run profiles into one with aggregate stats.
func AggregateProfiles(runs []Profile) Profile {
	if len(runs) == 0 {
		return Profile{SchemaVersion: SchemaVersion}
	}
	if len(runs) == 1 {
		p := runs[0]
		p.Runs = 1
		return p
	}
	base := runs[0]
	base.Runs = len(runs)
	base.Mode = "live"

	wholeFile := sampleFloats(runs, func(p Profile) float64 { return p.Read.WholeFileRatio })
	reRead := sampleFloats(runs, func(p Profile) float64 { return p.Read.ReReadRatio })
	prompt := sampleFloats(runs, func(p Profile) float64 { return float64(p.TokenSpend.PromptTokens) })
	completion := sampleFloats(runs, func(p Profile) float64 { return float64(p.TokenSpend.CompletionTokens) })
	cacheHit := sampleFloats(runs, func(p Profile) float64 { return p.Cache.HitRate })
	workersComplete := sampleFloats(runs, func(p Profile) float64 { return float64(p.TaskSuccess.WorkersComplete) })
	summarizeCalls := sampleFloats(runs, func(p Profile) float64 { return float64(p.Survey.SummarizeCalls) })
	mixedSurveyBatches := sampleFloats(runs, func(p Profile) float64 { return float64(p.Survey.MixedSurveyBatches) })
	repeatedSummarizeScopes := sampleFloats(runs, func(p Profile) float64 { return float64(p.Survey.RepeatedSummarizeScopes) })

	base.Aggregates = &Aggregates{
		Runs:                    len(runs),
		WholeFileRatio:          summarize(wholeFile),
		ReReadRatio:             summarize(reRead),
		PromptTokens:            summarize(prompt),
		CompletionTokens:        summarize(completion),
		CacheHitRate:            summarize(cacheHit),
		WorkersComplete:         summarize(workersComplete),
		SummarizeCalls:          summarize(summarizeCalls),
		MixedSurveyBatches:      summarize(mixedSurveyBatches),
		RepeatedSummarizeScopes: summarize(repeatedSummarizeScopes),
		SealedTerminalCalls: summarize(sampleFloats(runs, func(p Profile) float64 {
			return float64(p.Visual.SealedTerminalCalls)
		})),
		HeldSnapshotCalls: summarize(sampleFloats(runs, func(p Profile) float64 {
			return float64(p.Visual.HeldSnapshotCalls)
		})),
		HeldScreenCalls: summarize(sampleFloats(runs, func(p Profile) float64 {
			return float64(p.Visual.HeldScreenCalls)
		})),
		PageCaptureCalls: summarize(sampleFloats(runs, func(p Profile) float64 {
			return float64(p.Visual.PageCaptureCalls)
		})),
	}
	return base
}

func sampleFloats(runs []Profile, pick func(Profile) float64) []float64 {
	out := make([]float64, len(runs))
	for i := range runs {
		out[i] = pick(runs[i])
	}
	return out
}

func summarize(values []float64) StatSummary {
	n := len(values)
	if n == 0 {
		return StatSummary{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mean := 0.0
	for _, v := range sorted {
		mean += v
	}
	mean /= float64(n)
	variance := 0.0
	for _, v := range sorted {
		d := v - mean
		variance += d * d
	}
	stddev := 0.0
	if n > 1 {
		stddev = math.Sqrt(variance / float64(n))
	}
	cv := 0.0
	if mean != 0 {
		cv = stddev / math.Abs(mean)
	}
	return StatSummary{
		Mean:   mean,
		Median: percentile(sorted, 0.5),
		StdDev: stddev,
		Min:    sorted[0],
		Max:    sorted[n-1],
		P50:    percentile(sorted, 0.5),
		P90:    percentile(sorted, 0.9),
		N:      n,
		CV:     cv,
	}
}

func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	idx := p * float64(n-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}
