package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type benchSample struct {
	NsPerOp     float64
	BytesPerOp  float64
	AllocsPerOp float64
}

type benchStats struct {
	Samples      int
	MedianNs     float64
	MedianBytes  float64
	MedianAllocs float64
}

type metricEval struct {
	Benchmark string
	Metric    string
	Base      float64
	Head      float64
	Delta     float64
	Missing   bool
	Failed    bool
}

var (
	benchLineRe = regexp.MustCompile(`^Benchmark[^\s]+`)
	suffixNumRe = regexp.MustCompile(`-\d+$`)
)

const (
	minNsOpFloor     = 1.0 // sub-nanosecond jitter on fast benchmarks is CI noise, not regression
	minBytesOpFloor  = 256.0
	minAllocsOpFloor = 1.0
)

func main() {
	var (
		basePath            string
		headPath            string
		benchmarksRaw       string
		maxNsRegression     float64
		maxBytesRegression  float64
		maxAllocsRegression float64
		baseRSSPath         string
		headRSSPath         string
		maxRSSRegression    float64
		exitOnFail          bool
		postDefinitive      bool
		definitiveOutput    string
		definitiveWinsRaw   string
		definitiveWinNs     float64
		definitiveWinBytes  float64
		definitiveWinAllocs float64
	)

	flag.StringVar(&basePath, "base", "", "base benchmark output path")
	flag.StringVar(&headPath, "head", "", "head benchmark output path")
	flag.StringVar(&benchmarksRaw, "benchmarks", "BenchmarkGoParseFullDFA,BenchmarkGoParseIncrementalSingleByteEditDFA,BenchmarkGoParseIncrementalNoEditDFA", "comma-separated benchmark names to gate")
	flag.Float64Var(&maxNsRegression, "max-ns-regression", 0.08, "max allowed ns/op regression ratio (0.08 = +8%)")
	flag.Float64Var(&maxBytesRegression, "max-bytes-regression", 0.05, "max allowed B/op regression ratio (0.05 = +5%)")
	flag.Float64Var(&maxAllocsRegression, "max-allocs-regression", 0.05, "max allowed allocs/op regression ratio (0.05 = +5%)")
	flag.StringVar(&baseRSSPath, "base-rss", "", "optional /usr/bin/time -v output for base")
	flag.StringVar(&headRSSPath, "head-rss", "", "optional /usr/bin/time -v output for head")
	flag.Float64Var(&maxRSSRegression, "max-rss-regression", 0.10, "max allowed max-RSS regression ratio (0.10 = +10%)")
	flag.BoolVar(&exitOnFail, "exit-on-fail", true, "exit non-zero when regression gate fails")
	flag.BoolVar(&postDefinitive, "post-definitive", false, "emit definitive win/loss classification")
	flag.StringVar(&definitiveOutput, "definitive-output", "", "optional markdown output path for definitive classification")
	flag.StringVar(&definitiveWinsRaw, "definitive-win-benchmarks", "BenchmarkGoParseFullDFA,BenchmarkGoParseIncrementalSingleByteEditDFA", "comma-separated benchmarks required to declare definitive win")
	flag.Float64Var(&definitiveWinNs, "definitive-win-ns", 0.03, "required ns/op improvement ratio for definitive win (0.03 = -3%)")
	flag.Float64Var(&definitiveWinBytes, "definitive-win-bytes-regression", 0.0, "max allowed B/op regression ratio for definitive win (0.0 = no regression)")
	flag.Float64Var(&definitiveWinAllocs, "definitive-win-allocs-regression", 0.0, "max allowed allocs/op regression ratio for definitive win (0.0 = no regression)")
	flag.Parse()

	if strings.TrimSpace(basePath) == "" || strings.TrimSpace(headPath) == "" {
		fatalf("both -base and -head are required")
	}

	required := parseBenchmarks(benchmarksRaw)
	if len(required) == 0 {
		fatalf("-benchmarks must include at least one benchmark")
	}
	definitiveWins := parseBenchmarks(definitiveWinsRaw)
	if postDefinitive && len(definitiveWins) == 0 {
		fatalf("-definitive-win-benchmarks must include at least one benchmark when -post-definitive is set")
	}

	baseRaw, err := parseBenchFile(basePath)
	if err != nil {
		fatalf("parse base benchmarks: %v", err)
	}
	headRaw, err := parseBenchFile(headPath)
	if err != nil {
		fatalf("parse head benchmarks: %v", err)
	}

	base := aggregate(baseRaw)
	head := aggregate(headRaw)

	fmt.Printf("benchgate thresholds: ns<=+%.2f%% B<=+%.2f%% allocs<=+%.2f%% (min +%.0fns, +%.0f B/op, +%.0f alloc/op floors)\n",
		maxNsRegression*100.0, maxBytesRegression*100.0, maxAllocsRegression*100.0, minNsOpFloor, minBytesOpFloor, minAllocsOpFloor)
	fmt.Println("benchmark\tmetric\tbase\thead\tdelta\tstatus")

	failed := false
	evals := make([]metricEval, 0, len(required)*3+1)
	for _, name := range required {
		baseStats, ok := base[name]
		if !ok {
			fmt.Printf("%s\t-\t-\t-\t-\tFAIL (missing in base)\n", name)
			failed = true
			evals = append(evals,
				metricEval{Benchmark: name, Metric: "ns/op", Missing: true, Failed: true},
				metricEval{Benchmark: name, Metric: "B/op", Missing: true, Failed: true},
				metricEval{Benchmark: name, Metric: "allocs/op", Missing: true, Failed: true},
			)
			continue
		}
		headStats, ok := head[name]
		if !ok {
			fmt.Printf("%s\t-\t-\t-\t-\tFAIL (missing in head)\n", name)
			failed = true
			evals = append(evals,
				metricEval{Benchmark: name, Metric: "ns/op", Missing: true, Failed: true},
				metricEval{Benchmark: name, Metric: "B/op", Missing: true, Failed: true},
				metricEval{Benchmark: name, Metric: "allocs/op", Missing: true, Failed: true},
			)
			continue
		}
		if baseStats.Samples == 0 || headStats.Samples == 0 {
			fmt.Printf("%s\t-\t-\t-\t-\tFAIL (no samples)\n", name)
			failed = true
			evals = append(evals,
				metricEval{Benchmark: name, Metric: "ns/op", Missing: true, Failed: true},
				metricEval{Benchmark: name, Metric: "B/op", Missing: true, Failed: true},
				metricEval{Benchmark: name, Metric: "allocs/op", Missing: true, Failed: true},
			)
			continue
		}

		nsEval := evaluateMetric(name, "ns/op", baseStats.MedianNs, headStats.MedianNs, maxNsRegression)
		bytesEval := evaluateMetric(name, "B/op", baseStats.MedianBytes, headStats.MedianBytes, maxBytesRegression)
		allocsEval := evaluateMetric(name, "allocs/op", baseStats.MedianAllocs, headStats.MedianAllocs, maxAllocsRegression)
		printMetric(nsEval)
		printMetric(bytesEval)
		printMetric(allocsEval)
		failed = nsEval.Failed || bytesEval.Failed || allocsEval.Failed || failed
		evals = append(evals, nsEval, bytesEval, allocsEval)
	}

	if baseRSSPath != "" || headRSSPath != "" {
		if baseRSSPath == "" || headRSSPath == "" {
			fatalf("both -base-rss and -head-rss are required when either is set")
		}
		baseRSS, err := parseMaxRSS(baseRSSPath)
		if err != nil {
			fatalf("parse base RSS: %v", err)
		}
		headRSS, err := parseMaxRSS(headRSSPath)
		if err != nil {
			fatalf("parse head RSS: %v", err)
		}
		rssEval := evaluateMetric("rss", "max_rss_kb", float64(baseRSS), float64(headRSS), maxRSSRegression)
		printMetric(rssEval)
		evals = append(evals, rssEval)
		failed = rssEval.Failed || failed
	}

	if postDefinitive {
		outcome, reason := classifyDefinitive(required, definitiveWins, evals, failed, definitiveWinNs, definitiveWinBytes, definitiveWinAllocs)
		fmt.Printf("definitive_outcome\t%s\t%s\n", outcome, reason)
		if outcome == "DEFINITIVE_WIN" || outcome == "DEFINITIVE_LOSS" {
			md := renderDefinitiveMarkdown(outcome, reason, evals)
			fmt.Print(md)
			if definitiveOutput != "" {
				if err := os.WriteFile(definitiveOutput, []byte(md), 0o644); err != nil {
					fatalf("write definitive output: %v", err)
				}
			}
		}
	}

	if failed && exitOnFail {
		os.Exit(1)
	}
}

func parseBenchmarks(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func parseBenchFile(path string) (map[string][]benchSample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string][]benchSample{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !benchLineRe.MatchString(line) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		name := suffixNumRe.ReplaceAllString(fields[0], "")
		sample := benchSample{}
		for i := 2; i+1 < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				continue
			}
			switch fields[i+1] {
			case "ns/op":
				sample.NsPerOp = v
			case "B/op":
				sample.BytesPerOp = v
			case "allocs/op":
				sample.AllocsPerOp = v
			}
		}
		out[name] = append(out[name], sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func aggregate(raw map[string][]benchSample) map[string]benchStats {
	out := make(map[string]benchStats, len(raw))
	for name, runs := range raw {
		ns := make([]float64, 0, len(runs))
		bytes := make([]float64, 0, len(runs))
		allocs := make([]float64, 0, len(runs))
		for _, s := range runs {
			if s.NsPerOp > 0 {
				ns = append(ns, s.NsPerOp)
			}
			if s.BytesPerOp > 0 {
				bytes = append(bytes, s.BytesPerOp)
			}
			if s.AllocsPerOp > 0 {
				allocs = append(allocs, s.AllocsPerOp)
			}
		}
		out[name] = benchStats{
			Samples:      len(runs),
			MedianNs:     median(ns),
			MedianBytes:  median(bytes),
			MedianAllocs: median(allocs),
		}
	}
	return out
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	ys := make([]float64, len(xs))
	copy(ys, xs)
	sort.Float64s(ys)
	mid := len(ys) / 2
	if len(ys)%2 == 1 {
		return ys[mid]
	}
	return (ys[mid-1] + ys[mid]) / 2.0
}

func evaluateMetric(name, metric string, base, head, maxRegression float64) metricEval {
	ev := metricEval{
		Benchmark: name,
		Metric:    metric,
		Base:      base,
		Head:      head,
	}
	// Zero/zero is valid for some metrics (e.g. B/op and allocs/op on no-edit fast paths).
	if base == 0 && head == 0 {
		return ev
	}
	if base <= 0 || head <= 0 {
		ev.Missing = true
		ev.Failed = true
		return ev
	}

	ev.Delta = (head / base) - 1.0
	if metric == "B/op" {
		// For low-byte benchmarks, percentage-only gates are too sensitive
		// (e.g. 427->587 B/op is +37% but only +160 B/op). Apply a minimum
		// absolute slack alongside the ratio threshold.
		allowedAbs := base * maxRegression
		if allowedAbs < minBytesOpFloor {
			allowedAbs = minBytesOpFloor
		}
		if (head - base) > allowedAbs {
			ev.Failed = true
		}
		return ev
	}
	if metric == "allocs/op" {
		// For low-allocation benchmarks, pure percentage gates are too strict
		// (e.g. 5->6 allocs/op is +20% but often acceptable when latency/RSS
		// improve). Apply a minimum absolute slack of +1 alloc/op in addition
		// to the ratio threshold.
		allowedAbs := base * maxRegression
		if allowedAbs < minAllocsOpFloor {
			allowedAbs = minAllocsOpFloor
		}
		if (head - base) > allowedAbs {
			ev.Failed = true
		}
		return ev
	}
	// For very fast benchmarks (e.g. ~8ns), percentage-only gates are too
	// sensitive to CI noise. Apply a minimum absolute ns slack so sub-ns
	// jitter doesn't trip the gate.
	allowedAbsNs := base * maxRegression
	if allowedAbsNs < minNsOpFloor {
		allowedAbsNs = minNsOpFloor
	}
	if (head - base) > allowedAbsNs {
		ev.Failed = true
	}
	return ev
}

func printMetric(ev metricEval) {
	if ev.Missing {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\tFAIL (missing metric)\n",
			ev.Benchmark, ev.Metric, fmtFloat(ev.Base), fmtFloat(ev.Head), "-")
		return
	}
	status := "OK"
	if ev.Failed {
		status = "FAIL"
	}
	fmt.Printf("%s\t%s\t%s\t%s\t%+.2f%%\t%s\n",
		ev.Benchmark, ev.Metric, fmtFloat(ev.Base), fmtFloat(ev.Head), ev.Delta*100.0, status)
}

func classifyDefinitive(required, definitiveWins []string, evals []metricEval, gateFailed bool, winNs, winBytes, winAllocs float64) (string, string) {
	if gateFailed {
		return "DEFINITIVE_LOSS", "regression gate failed"
	}

	m := map[string]map[string]metricEval{}
	for _, ev := range evals {
		if _, ok := m[ev.Benchmark]; !ok {
			m[ev.Benchmark] = map[string]metricEval{}
		}
		m[ev.Benchmark][ev.Metric] = ev
	}

	for _, bench := range required {
		mm, ok := m[bench]
		if !ok {
			return "DEFINITIVE_LOSS", fmt.Sprintf("missing benchmark: %s", bench)
		}
		for _, metric := range []string{"ns/op", "B/op", "allocs/op"} {
			ev, ok := mm[metric]
			if !ok || ev.Missing {
				return "DEFINITIVE_LOSS", fmt.Sprintf("missing metric: %s %s", bench, metric)
			}
			if ev.Failed {
				return "DEFINITIVE_LOSS", fmt.Sprintf("regression: %s %s", bench, metric)
			}
		}
	}

	for _, bench := range required {
		ns, ok := m[bench]["ns/op"]
		if !ok || ns.Missing {
			return "INCONCLUSIVE", fmt.Sprintf("missing ns/op metric: %s", bench)
		}
		if ns.Delta > 0 {
			return "INCONCLUSIVE", fmt.Sprintf("ns/op regression present: %s (%+.2f%%)", bench, ns.Delta*100.0)
		}
	}

	for _, bench := range definitiveWins {
		mm, ok := m[bench]
		if !ok {
			return "INCONCLUSIVE", fmt.Sprintf("missing definitive benchmark: %s", bench)
		}

		ns, ok := mm["ns/op"]
		if !ok || ns.Missing {
			return "INCONCLUSIVE", fmt.Sprintf("missing ns/op metric: %s", bench)
		}
		if ns.Delta > -winNs {
			return "INCONCLUSIVE", fmt.Sprintf("ns/op gain below threshold: %s (%+.2f%%)", bench, ns.Delta*100.0)
		}

		bs, ok := mm["B/op"]
		if !ok || bs.Missing {
			return "INCONCLUSIVE", fmt.Sprintf("missing B/op metric: %s", bench)
		}
		if bs.Delta > winBytes {
			return "INCONCLUSIVE", fmt.Sprintf("B/op regression above threshold: %s (%+.2f%%)", bench, bs.Delta*100.0)
		}

		al, ok := mm["allocs/op"]
		if !ok || al.Missing {
			return "INCONCLUSIVE", fmt.Sprintf("missing allocs/op metric: %s", bench)
		}
		if al.Delta > winAllocs {
			return "INCONCLUSIVE", fmt.Sprintf("allocs/op regression above threshold: %s (%+.2f%%)", bench, al.Delta*100.0)
		}
	}

	return "DEFINITIVE_WIN", "all definitive-win benchmarks met latency and memory thresholds"
}

func renderDefinitiveMarkdown(outcome, reason string, evals []metricEval) string {
	var b strings.Builder
	b.WriteString("### Perf Definitive Outcome\n\n")
	b.WriteString(fmt.Sprintf("- outcome: `%s`\n", outcome))
	b.WriteString(fmt.Sprintf("- reason: %s\n\n", reason))
	b.WriteString("| benchmark | metric | base | head | delta |\n")
	b.WriteString("| --- | --- | ---: | ---: | ---: |\n")
	for _, ev := range evals {
		delta := "-"
		if !ev.Missing {
			delta = fmt.Sprintf("%+.2f%%", ev.Delta*100.0)
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			ev.Benchmark, ev.Metric, fmtFloat(ev.Base), fmtFloat(ev.Head), delta))
	}
	b.WriteString("\n")
	return b.String()
}

func parseMaxRSS(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "Maximum resident set size (kbytes):") {
			continue
		}
		idx := strings.LastIndex(line, ":")
		if idx < 0 || idx+1 >= len(line) {
			return 0, fmt.Errorf("unexpected max RSS line format: %q", line)
		}
		v, err := strconv.ParseInt(strings.TrimSpace(line[idx+1:]), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse max RSS %q: %w", line, err)
		}
		return v, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("max RSS line not found in %s", path)
}

func fmtFloat(v float64) string {
	if v == 0 {
		return "0"
	}
	if v >= 1000 {
		return fmt.Sprintf("%.0f", v)
	}
	if v >= 10 {
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.4f", v)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
