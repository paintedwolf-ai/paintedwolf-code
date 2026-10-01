package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type step struct {
	Name    string
	Dir     string
	Env     map[string]string
	Command []string
	LogPath string
}

type stepResult struct {
	Step     step
	Duration time.Duration
	Err      error
}

func main() {
	var (
		mode                    string
		outDir                  string
		runRootTests            bool
		runCgoParity            bool
		runPerf                 bool
		parityRunRegex          string
		parityTags              string
		benchRegex              string
		benchCount              int
		benchBenchtime          string
		benchBase               string
		maxNSRegression         float64
		maxBytesRegression      float64
		maxAllocsRegression     float64
		goMaxProcs              int
		realCorpusDir           string
		realCorpusLangs         string
		realCorpusManifest      string
		realCorpusResultPath    string
		realCorpusArtifactDir   string
		realCorpusArtifactMode  string
		realCorpusOracleTimeout int
		realCorpusGCAfterFile   bool
		realCorpusSplitByLang   bool
		realCorpusSkipOracleErr bool
		realCorpusScoreboardMD  string
		realCorpusBoardJSON     string
		realCorpusBoardMD       string
		realCorpusL4Limit       int
		realCorpusL4Languages   string
		confidenceManifestPath  string
		confidenceProfile       string
		confidenceResultsPath   string
		confidenceMinScore      float64
		confidenceIgnoreMiss    bool
	)

	flag.StringVar(&mode, "mode", "all", "one of: all, correctness, perf")
	flag.StringVar(&outDir, "out-dir", "harness_out", "artifact output directory")
	flag.BoolVar(&runRootTests, "root-tests", true, "run root go test ./... -count=1")
	flag.BoolVar(&runCgoParity, "cgo-parity", true, "run cgo_harness parity gate")
	flag.BoolVar(&runPerf, "perf", true, "run benchmark trio and optional benchgate compare")
	flag.StringVar(&parityRunRegex, "parity-run", "^TestParityFreshParse$|^TestParityIncrementalParse$|^TestParityHasNoErrors$|^TestParityIssue3Repros$|^TestParityGLRCanaryGo$|^TestParityGLRCanarySet$|^TestParityGLRCapPressureTopLanguages$|^TestParityHighlight$|^TestParityScalaRealWorldCorpus$", "regex passed to cgo parity go test -run")
	flag.StringVar(&parityTags, "parity-tags", "treesitter_c_parity", "build tags for cgo parity tests")
	flag.StringVar(&benchRegex, "bench-regex", "^(BenchmarkGoParseFullDFA|BenchmarkGoParseIncrementalSingleByteEditDFA|BenchmarkGoParseIncrementalNoEditDFA)$", "benchmark regex for perf run")
	flag.IntVar(&benchCount, "bench-count", 10, "benchmark count")
	flag.StringVar(&benchBenchtime, "bench-benchtime", "750ms", "benchmark benchtime")
	flag.StringVar(&benchBase, "bench-base", "", "optional base benchmark output path for benchgate comparison")
	flag.Float64Var(&maxNSRegression, "max-ns-regression", 0.08, "max allowed ns/op regression ratio for benchgate")
	flag.Float64Var(&maxBytesRegression, "max-bytes-regression", 0.05, "max allowed B/op regression ratio for benchgate")
	flag.Float64Var(&maxAllocsRegression, "max-allocs-regression", 0.05, "max allowed allocs/op regression ratio for benchgate")
	flag.IntVar(&goMaxProcs, "gomaxprocs", 1, "GOMAXPROCS used for benchmarks")
	flag.StringVar(&realCorpusDir, "real-corpus-dir", "", "optional real corpus root; when set, run cgo_harness/cmd/corpus_parity")
	flag.StringVar(&realCorpusLangs, "real-corpus-langs", "top10", "value passed to corpus_parity --lang")
	flag.StringVar(&realCorpusManifest, "real-corpus-manifest", "", "optional manifest.json used to build an L3/L4 real corpus board after parity")
	flag.StringVar(&realCorpusResultPath, "real-corpus-out", "", "optional explicit corpus JSONL output path")
	flag.StringVar(&realCorpusArtifactDir, "real-corpus-artifacts", "", "optional explicit corpus artifact dir")
	flag.StringVar(&realCorpusArtifactMode, "real-corpus-artifact-mode", "failures", "artifact emission mode passed to corpus_parity: all|failures")
	flag.IntVar(&realCorpusOracleTimeout, "real-corpus-oracle-timeout-ms", 5000, "if >0, set a per-file timeout for pinned C oracle parses during real-corpus parity so pathological oracle runs become exclusions instead of board stoppers")
	flag.BoolVar(&realCorpusGCAfterFile, "real-corpus-gc-after-file", true, "when true, ask corpus_parity to force GC after each real-corpus file to reduce peak RSS")
	flag.BoolVar(&realCorpusSplitByLang, "real-corpus-split-by-language", true, "when true and -real-corpus-langs is an explicit language list, run corpus_parity once per language and append into one combined JSONL")
	flag.BoolVar(&realCorpusSkipOracleErr, "real-corpus-skip-go-on-oracle-error", true, "when true, skip gotreesitter parsing for real-corpus files whose C oracle root already has parse errors")
	flag.StringVar(&realCorpusScoreboardMD, "real-corpus-scoreboard", "", "optional explicit corpus scoreboard markdown path")
	flag.StringVar(&realCorpusBoardJSON, "real-corpus-board-json", "", "optional explicit real corpus board JSON output path")
	flag.StringVar(&realCorpusBoardMD, "real-corpus-board-md", "", "optional explicit real corpus board markdown output path")
	flag.IntVar(&realCorpusL4Limit, "real-corpus-l4-limit", 0, "if >0, treat L4 as the top N languages by max large-file bytes from the manifest")
	flag.StringVar(&realCorpusL4Languages, "real-corpus-l4-languages", "", "optional comma-separated explicit L4 language subset for board generation")
	flag.StringVar(&confidenceManifestPath, "confidence-manifest", "", "optional path to weighted confidence manifest JSON")
	flag.StringVar(&confidenceProfile, "confidence-profile", "", "optional built-in confidence profile: top50|core90")
	flag.StringVar(&confidenceResultsPath, "confidence-results", "", "optional JSONL path for confidence scoring; defaults to real-corpus output")
	flag.Float64Var(&confidenceMinScore, "confidence-min", 0.90, "minimum weighted confidence score required to pass")
	flag.BoolVar(&confidenceIgnoreMiss, "confidence-ignore-missing", false, "ignore manifest languages missing from results when scoring")
	flag.Parse()

	switch mode {
	case "all":
		// honor individual flags
	case "correctness":
		runPerf = false
	case "perf":
		runRootTests = false
		runCgoParity = false
	default:
		fatalf("invalid -mode %q (want all|correctness|perf)", mode)
	}

	if benchCount <= 0 {
		fatalf("-bench-count must be > 0")
	}
	if strings.TrimSpace(benchBenchtime) == "" {
		fatalf("-bench-benchtime must be non-empty")
	}
	if confidenceMinScore <= 0 || confidenceMinScore > 1 {
		fatalf("-confidence-min must be within (0,1], got %.4f", confidenceMinScore)
	}
	if strings.TrimSpace(confidenceManifestPath) != "" && strings.TrimSpace(confidenceProfile) != "" {
		fatalf("set only one of -confidence-manifest or -confidence-profile")
	}
	if realCorpusL4Limit < 0 {
		fatalf("-real-corpus-l4-limit must be >= 0")
	}
	switch mode := strings.TrimSpace(realCorpusArtifactMode); mode {
	case "", "all", "failures":
	default:
		fatalf("invalid -real-corpus-artifact-mode %q (want all|failures)", realCorpusArtifactMode)
	}
	if realCorpusOracleTimeout < 0 {
		fatalf("-real-corpus-oracle-timeout-ms must be >= 0")
	}
	if realCorpusL4Limit > 0 && strings.TrimSpace(realCorpusL4Languages) != "" {
		fatalf("set only one of -real-corpus-l4-limit or -real-corpus-l4-languages")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatalf("create out dir: %v", err)
	}

	results := make([]stepResult, 0, 8)
	appendResult := func(res stepResult) {
		results = append(results, res)
	}

	resolvedRealCorpusOut := ""

	if runRootTests {
		appendResult(runStep(step{
			Name:    "root-tests",
			Dir:     ".",
			Command: []string{"go", "test", "./...", "-count=1"},
			LogPath: filepath.Join(outDir, "01_root_tests.log"),
		}))
	}

	if runCgoParity {
		appendResult(runStep(step{
			Name:    "cgo-parity-gate",
			Dir:     "cgo_harness",
			Command: []string{"go", "test", ".", "-tags", parityTags, "-run", parityRunRegex, "-count=1", "-v"},
			LogPath: filepath.Join(outDir, "02_cgo_parity.log"),
		}))
	}

	if strings.TrimSpace(realCorpusDir) != "" {
		outJSONL := realCorpusResultPath
		if strings.TrimSpace(outJSONL) == "" {
			outJSONL = harnessSubprocessOutputPath(outDir, "03_real_corpus_results.jsonl")
		}
		resolvedRealCorpusOut = mustAbs(outJSONL)
		artifactDir := realCorpusArtifactDir
		if strings.TrimSpace(artifactDir) == "" {
			artifactDir = harnessSubprocessOutputPath(outDir, "03_real_corpus_dump_v1")
		}
		artifactDir = mustAbs(artifactDir)
		scoreboard := realCorpusScoreboardMD
		if strings.TrimSpace(scoreboard) == "" {
			scoreboard = harnessSubprocessOutputPath(outDir, "03_real_corpus_PARITY.md")
		}
		manifestPath := strings.TrimSpace(realCorpusManifest)
		if manifestPath == "" {
			candidate := filepath.Join(realCorpusDir, "manifest.json")
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				manifestPath = candidate
			}
		}
		splitLangs := resolveRealCorpusSplitLanguages(realCorpusLangs)
		if realCorpusSplitByLang && len(splitLangs) > 1 {
			if err := os.Remove(resolvedRealCorpusOut); err != nil && !errors.Is(err, os.ErrNotExist) {
				fatalf("remove %s: %v", resolvedRealCorpusOut, err)
			}
			for i, lang := range splitLangs {
				langOut := mustAbs(filepath.Join(outDir, fmt.Sprintf("03_real_corpus_results_%02d_%s.jsonl", i+1, safeStepName(lang))))
				langLog := filepath.Join(outDir, fmt.Sprintf("03_cgo_real_corpus_%02d_%s.log", i+1, safeStepName(lang)))
				langStep := step{
					Name: fmt.Sprintf("cgo-real-corpus-parity/%s", lang),
					Dir:  "cgo_harness",
					Command: []string{
						"go", "run", "-tags", parityTags, "./cmd/corpus_parity",
						"--lang", lang,
						"--corpus", realCorpusDir,
						"--out", langOut,
						"--artifact-dir", artifactDir,
						"--artifact-mode", realCorpusArtifactMode,
						"--scoreboard", "",
					},
					LogPath: langLog,
				}
				if realCorpusOracleTimeout > 0 {
					langStep.Command = append(langStep.Command, "--oracle-timeout-ms", fmt.Sprintf("%d", realCorpusOracleTimeout))
				}
				if realCorpusSkipOracleErr {
					langStep.Command = append(langStep.Command, "--skip-go-on-oracle-error")
				}
				if realCorpusGCAfterFile {
					langStep.Command = append(langStep.Command, "--gc-after-file")
				}
				langRes := runStep(langStep)
				appendResult(langRes)
				if langRes.Err != nil {
					break
				}
				if err := appendFileContents(resolvedRealCorpusOut, langOut); err != nil {
					appendResult(stepResult{
						Step: step{
							Name:    fmt.Sprintf("cgo-real-corpus-append/%s", lang),
							LogPath: langLog,
						},
						Err: err,
					})
					break
				}
			}
			if strings.TrimSpace(scoreboard) != "" {
				scoreboard = mustAbs(scoreboard)
				if err := writeSplitRealCorpusScoreboard(scoreboard, splitLangs, resolvedRealCorpusOut); err != nil {
					fatalf("write split real corpus scoreboard: %v", err)
				}
			}
		} else {
			realCorpusStep := step{
				Name: "cgo-real-corpus-parity",
				Dir:  "cgo_harness",
				Command: []string{
					"go", "run", "-tags", parityTags, "./cmd/corpus_parity",
					"--lang", realCorpusLangs,
					"--corpus", realCorpusDir,
					"--out", resolvedRealCorpusOut,
					"--artifact-dir", artifactDir,
					"--artifact-mode", realCorpusArtifactMode,
					"--scoreboard", mustAbs(scoreboard),
				},
				LogPath: filepath.Join(outDir, "03_cgo_real_corpus.log"),
			}
			if realCorpusOracleTimeout > 0 {
				realCorpusStep.Command = append(realCorpusStep.Command, "--oracle-timeout-ms", fmt.Sprintf("%d", realCorpusOracleTimeout))
			}
			if realCorpusSkipOracleErr {
				realCorpusStep.Command = append(realCorpusStep.Command, "--skip-go-on-oracle-error")
			}
			if realCorpusGCAfterFile {
				realCorpusStep.Command = append(realCorpusStep.Command, "--gc-after-file")
			}
			appendResult(runStep(realCorpusStep))
		}
		if manifestPath != "" {
			manifestPath = mustAbs(manifestPath)
			boardJSON := realCorpusBoardJSON
			if strings.TrimSpace(boardJSON) == "" {
				boardJSON = filepath.Join(outDir, "03_real_corpus_board.json")
			}
			boardJSON = mustAbs(boardJSON)
			boardMD := realCorpusBoardMD
			if strings.TrimSpace(boardMD) == "" {
				boardMD = filepath.Join(outDir, "03_real_corpus_board.md")
			}
			boardMD = mustAbs(boardMD)
			outJSONL = resolvedRealCorpusOut
			cmd := []string{
				"go", "run", "./cmd/real_corpus_board",
				"--manifest", manifestPath,
				"--results", outJSONL,
				"--out-json", boardJSON,
				"--out-md", boardMD,
			}
			if realCorpusL4Limit > 0 {
				cmd = append(cmd, "--l4-limit", fmt.Sprintf("%d", realCorpusL4Limit))
			}
			if strings.TrimSpace(realCorpusL4Languages) != "" {
				cmd = append(cmd, "--l4-languages", realCorpusL4Languages)
			}
			appendResult(runStep(step{
				Name:    "real-corpus-board",
				Dir:     "cgo_harness",
				Command: cmd,
				LogPath: filepath.Join(outDir, "03b_real_corpus_board.log"),
			}))
		}
	}

	if strings.TrimSpace(confidenceManifestPath) != "" || strings.TrimSpace(confidenceProfile) != "" {
		var (
			manifest confidenceManifest
			err      error
		)
		if strings.TrimSpace(confidenceManifestPath) != "" {
			manifest, err = confidenceManifestFromPath(confidenceManifestPath)
			if err != nil {
				fatalf("load confidence manifest: %v", err)
			}
		} else {
			manifest, err = confidenceManifestFromProfile(confidenceProfile)
			if err != nil {
				fatalf("load confidence profile: %v", err)
			}
		}
		resultsPath := strings.TrimSpace(confidenceResultsPath)
		if resultsPath == "" {
			resultsPath = strings.TrimSpace(resolvedRealCorpusOut)
		}
		if resultsPath == "" {
			fatalf("confidence gate requires results JSONL; set -confidence-results or -real-corpus-out/-real-corpus-dir")
		}
		appendResult(runConfidenceStep(
			"confidence-gate",
			manifest,
			resultsPath,
			confidenceMinScore,
			confidenceIgnoreMiss,
			filepath.Join(outDir, "06_confidence.log"),
		))
	}

	benchHeadPath := filepath.Join(outDir, "04_perf_head.txt")
	if runPerf {
		env := map[string]string{}
		if goMaxProcs > 0 {
			env["GOMAXPROCS"] = fmt.Sprintf("%d", goMaxProcs)
		}
		appendResult(runStep(step{
			Name:    "perf-bench-trio",
			Dir:     ".",
			Env:     env,
			Command: []string{"go", "test", ".", "-run", "^$", "-bench", benchRegex, "-benchmem", "-count", fmt.Sprintf("%d", benchCount), "-benchtime", benchBenchtime},
			LogPath: benchHeadPath,
		}))
		if strings.TrimSpace(benchBase) != "" {
			appendResult(runStep(step{
				Name: "perf-benchgate-compare",
				Dir:  ".",
				Command: []string{
					"go", "run", "./cmd/benchgate",
					"-base", benchBase,
					"-head", benchHeadPath,
					"-max-ns-regression", fmt.Sprintf("%.6f", maxNSRegression),
					"-max-bytes-regression", fmt.Sprintf("%.6f", maxBytesRegression),
					"-max-allocs-regression", fmt.Sprintf("%.6f", maxAllocsRegression),
				},
				LogPath: filepath.Join(outDir, "05_perf_benchgate.log"),
			}))
		}
	}

	summaryPath := filepath.Join(outDir, "SUMMARY.md")
	if err := writeSummary(summaryPath, results); err != nil {
		fatalf("write summary: %v", err)
	}
	fmt.Printf("\nHarness summary: %s\n", summaryPath)

	for _, res := range results {
		if res.Err != nil {
			os.Exit(1)
		}
	}
}

func runStep(s step) stepResult {
	start := time.Now()
	fmt.Printf("\n==> %s\n", s.Name)
	if len(s.Command) == 0 {
		return stepResult{Step: s, Duration: time.Since(start), Err: errors.New("empty command")}
	}

	if err := os.MkdirAll(filepath.Dir(s.LogPath), 0o755); err != nil {
		return stepResult{Step: s, Duration: time.Since(start), Err: err}
	}
	logFile, err := os.Create(s.LogPath)
	if err != nil {
		return stepResult{Step: s, Duration: time.Since(start), Err: err}
	}
	defer logFile.Close()

	cmd := exec.Command(s.Command[0], s.Command[1:]...)
	if strings.TrimSpace(s.Dir) != "" {
		cmd.Dir = s.Dir
	}
	cmd.Env = os.Environ()
	for k, v := range s.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	mw := io.MultiWriter(os.Stdout, logFile)
	cmd.Stdout = mw
	cmd.Stderr = mw

	err = cmd.Run()
	dur := time.Since(start)
	if err != nil {
		fmt.Printf("FAIL %s (%s) -> %s\n", s.Name, dur.Round(time.Millisecond), s.LogPath)
		return stepResult{Step: s, Duration: dur, Err: err}
	}
	fmt.Printf("PASS %s (%s) -> %s\n", s.Name, dur.Round(time.Millisecond), s.LogPath)
	return stepResult{Step: s, Duration: dur}
}

func writeSummary(path string, results []stepResult) error {
	var b strings.Builder
	b.WriteString("# Harness Summary\n\n")
	for _, res := range results {
		status := "PASS"
		if res.Err != nil {
			status = "FAIL"
		}
		b.WriteString(fmt.Sprintf("- `%s` %s (%s) log: `%s`\n", status, res.Step.Name, res.Duration.Round(time.Millisecond), res.Step.LogPath))
		if res.Err != nil {
			b.WriteString(fmt.Sprintf("  - error: `%v`\n", res.Err))
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "harnessgate: "+format+"\n", args...)
	os.Exit(2)
}

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		fatalf("resolve absolute path for %q: %v", path, err)
	}
	return abs
}

func resolveRealCorpusSplitLanguages(raw string) []string {
	value := strings.TrimSpace(raw)
	switch value {
	case "", "top", "top10", "top50":
		return nil
	default:
		return parseCSVList(raw)
	}
}

func parseCSVList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 16)
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func safeStepName(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return "unnamed"
	}
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ":", "_")
	return s
}

func appendFileContents(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func writeSplitRealCorpusScoreboard(path string, langs []string, resultsPath string) error {
	var b strings.Builder
	b.WriteString("# PARITY\n\n")
	b.WriteString("_Generated by harnessgate split-by-language real-corpus run._\n\n")
	b.WriteString("Combined results JSONL: `")
	b.WriteString(resultsPath)
	b.WriteString("`\n\n")
	if len(langs) > 0 {
		b.WriteString("Languages:\n")
		for _, lang := range langs {
			b.WriteString("- `")
			b.WriteString(lang)
			b.WriteString("`\n")
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func harnessSubprocessOutputPath(outDir, name string) string {
	if filepath.IsAbs(outDir) {
		return filepath.Join(outDir, name)
	}
	return filepath.Join("..", outDir, name)
}
