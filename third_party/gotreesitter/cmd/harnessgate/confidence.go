package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type confidenceLanguage struct {
	Name         string  `json:"name"`
	Weight       float64 `json:"weight"`
	MinPassRatio float64 `json:"min_pass_ratio,omitempty"`
	Required     bool    `json:"required,omitempty"`
}

type confidenceManifest struct {
	Languages []confidenceLanguage `json:"languages"`
}

type confidenceRow struct {
	Name         string
	Weight       float64
	MinPassRatio float64
	Required     bool
	Seen         int
	Passed       int
	PassRatio    float64
	Pass         bool
}

type corpusParityJSONLRow struct {
	Language string `json:"language"`
	Pass     bool   `json:"pass"`
}

func confidenceManifestFromPath(path string) (confidenceManifest, error) {
	if strings.TrimSpace(path) == "" {
		return confidenceManifest{}, errors.New("empty confidence manifest path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return confidenceManifest{}, err
	}
	var m confidenceManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return confidenceManifest{}, fmt.Errorf("decode confidence manifest: %w", err)
	}
	return m, nil
}

func confidenceManifestFromProfile(profile string) (confidenceManifest, error) {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "":
		return confidenceManifest{}, errors.New("empty confidence profile")
	case "top50":
		return equalWeightManifest(top50ConfidenceLanguages), nil
	case "core90":
		core := make([]string, 0, len(top50ConfidenceLanguages)+len(core90ExtraConfidenceLanguages))
		seen := make(map[string]struct{}, len(top50ConfidenceLanguages)+len(core90ExtraConfidenceLanguages))
		for _, name := range top50ConfidenceLanguages {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			core = append(core, name)
		}
		for _, name := range core90ExtraConfidenceLanguages {
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			core = append(core, name)
		}
		return equalWeightManifest(core), nil
	default:
		return confidenceManifest{}, fmt.Errorf("unknown confidence profile %q (want top50 or core90)", profile)
	}
}

func equalWeightManifest(names []string) confidenceManifest {
	langs := make([]confidenceLanguage, 0, len(names))
	for _, name := range names {
		langs = append(langs, confidenceLanguage{
			Name:         name,
			Weight:       1.0,
			MinPassRatio: 1.0,
		})
	}
	return confidenceManifest{Languages: langs}
}

func runConfidenceStep(name string, manifest confidenceManifest, resultsPath string, minScore float64, ignoreMissing bool, logPath string) stepResult {
	start := time.Now()
	fmt.Printf("\n==> %s\n", name)

	if strings.TrimSpace(resultsPath) == "" {
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: time.Since(start),
			Err:      errors.New("confidence results path is empty"),
		}
	}
	if minScore <= 0 || minScore > 1 {
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: time.Since(start),
			Err:      fmt.Errorf("confidence min score must be within (0,1], got %.4f", minScore),
		}
	}
	if len(manifest.Languages) == 0 {
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: time.Since(start),
			Err:      errors.New("confidence manifest has no languages"),
		}
	}

	type agg struct {
		total int
		pass  int
	}
	byLang := make(map[string]agg)

	f, err := os.Open(resultsPath)
	if err != nil {
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: time.Since(start),
			Err:      fmt.Errorf("open confidence results: %w", err),
		}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row corpusParityJSONLRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return stepResult{
				Step:     step{Name: name, LogPath: logPath},
				Duration: time.Since(start),
				Err:      fmt.Errorf("decode confidence results jsonl: %w", err),
			}
		}
		if row.Language == "" {
			continue
		}
		a := byLang[row.Language]
		a.total++
		if row.Pass {
			a.pass++
		}
		byLang[row.Language] = a
	}
	if err := scanner.Err(); err != nil {
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: time.Since(start),
			Err:      fmt.Errorf("read confidence results: %w", err),
		}
	}

	rows := make([]confidenceRow, 0, len(manifest.Languages))
	var (
		totalWeight    float64
		passedWeight   float64
		requiredFailed []string
		missing        []string
	)

	for _, lang := range manifest.Languages {
		if strings.TrimSpace(lang.Name) == "" {
			return stepResult{
				Step:     step{Name: name, LogPath: logPath},
				Duration: time.Since(start),
				Err:      errors.New("confidence manifest contains empty language name"),
			}
		}
		if lang.Weight <= 0 {
			return stepResult{
				Step:     step{Name: name, LogPath: logPath},
				Duration: time.Since(start),
				Err:      fmt.Errorf("confidence manifest has non-positive weight for %q", lang.Name),
			}
		}
		minPass := lang.MinPassRatio
		if minPass <= 0 {
			minPass = 1.0
		}
		if minPass > 1 {
			return stepResult{
				Step:     step{Name: name, LogPath: logPath},
				Duration: time.Since(start),
				Err:      fmt.Errorf("confidence manifest min_pass_ratio > 1 for %q", lang.Name),
			}
		}

		a := byLang[lang.Name]
		row := confidenceRow{
			Name:         lang.Name,
			Weight:       lang.Weight,
			MinPassRatio: minPass,
			Required:     lang.Required,
			Seen:         a.total,
			Passed:       a.pass,
		}
		if row.Seen > 0 {
			row.PassRatio = float64(row.Passed) / float64(row.Seen)
		}
		row.Pass = row.Seen > 0 && row.PassRatio >= row.MinPassRatio
		if row.Seen == 0 {
			missing = append(missing, row.Name)
		}

		if !(ignoreMissing && row.Seen == 0) {
			totalWeight += row.Weight
			if row.Pass {
				passedWeight += row.Weight
			}
		}
		if row.Required && !row.Pass {
			requiredFailed = append(requiredFailed, row.Name)
		}
		rows = append(rows, row)
	}

	if totalWeight <= 0 {
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: time.Since(start),
			Err:      errors.New("confidence total weight is zero after applying missing-language policy"),
		}
	}
	confidence := passedWeight / totalWeight

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Pass != rows[j].Pass {
			return rows[i].Pass
		}
		if rows[i].Weight != rows[j].Weight {
			return rows[i].Weight > rows[j].Weight
		}
		return rows[i].Name < rows[j].Name
	})
	sort.Strings(requiredFailed)
	sort.Strings(missing)

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: time.Since(start),
			Err:      err,
		}
	}
	var report strings.Builder
	report.WriteString("# Confidence Gate\n\n")
	report.WriteString(fmt.Sprintf("- results: `%s`\n", resultsPath))
	report.WriteString(fmt.Sprintf("- min score: `%.4f`\n", minScore))
	report.WriteString(fmt.Sprintf("- ignore missing: `%v`\n", ignoreMissing))
	report.WriteString(fmt.Sprintf("- score: `%.4f`\n", confidence))
	report.WriteString(fmt.Sprintf("- weighted pass: `%.4f / %.4f`\n", passedWeight, totalWeight))
	if len(requiredFailed) > 0 {
		report.WriteString(fmt.Sprintf("- required failed: `%s`\n", strings.Join(requiredFailed, ", ")))
	}
	if len(missing) > 0 {
		report.WriteString(fmt.Sprintf("- missing languages: `%s`\n", strings.Join(missing, ", ")))
	}
	report.WriteString("\n| language | seen | pass | ratio | min | weight | required |\n")
	report.WriteString("|---|---:|---:|---:|---:|---:|:---:|\n")
	for _, row := range rows {
		report.WriteString(fmt.Sprintf(
			"| %s | %d | %d | %.3f | %.3f | %.3f | %v |\n",
			row.Name, row.Seen, row.Passed, row.PassRatio, row.MinPassRatio, row.Weight, row.Required,
		))
	}
	if err := os.WriteFile(logPath, []byte(report.String()), 0o644); err != nil {
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: time.Since(start),
			Err:      err,
		}
	}

	var gateErr error
	if len(requiredFailed) > 0 {
		gateErr = fmt.Errorf("required language confidence failures: %s", strings.Join(requiredFailed, ", "))
	} else if confidence < minScore {
		gateErr = fmt.Errorf("confidence %.4f below threshold %.4f", confidence, minScore)
	}

	dur := time.Since(start)
	if gateErr != nil {
		fmt.Printf("FAIL %s (%s) score=%.4f min=%.4f -> %s\n", name, dur.Round(time.Millisecond), confidence, minScore, logPath)
		return stepResult{
			Step:     step{Name: name, LogPath: logPath},
			Duration: dur,
			Err:      gateErr,
		}
	}
	fmt.Printf("PASS %s (%s) score=%.4f min=%.4f -> %s\n", name, dur.Round(time.Millisecond), confidence, minScore, logPath)
	return stepResult{
		Step:     step{Name: name, LogPath: logPath},
		Duration: dur,
	}
}

var top50ConfidenceLanguages = []string{
	"typescript", "tsx", "javascript", "python", "java", "c_sharp", "php", "bash", "cpp", "go",
	"html", "css", "sql", "c", "rust", "json", "ruby", "swift", "kotlin", "dart",
	"lua", "yaml", "xml", "toml", "markdown", "svelte", "scss", "powershell", "r", "scala",
	"hcl", "graphql", "perl", "elixir", "haskell", "julia", "clojure", "erlang", "ocaml", "nix",
	"objc", "gomod", "json5", "ini", "zig", "make", "cmake", "d", "awk", "elm",
}

// core90ExtraConfidenceLanguages extends top50 with conflict-heavy and scanner-
// sensitive languages to provide a broader weighted confidence surface.
var core90ExtraConfidenceLanguages = []string{
	"ada", "agda", "apex", "arduino", "astro", "authzed", "bitbake", "blade", "brightscript", "cairo",
	"chatito", "cobol", "commonlisp", "cuda", "doxygen", "earthfile", "editorconfig", "enforce", "fennel", "fsharp",
	"gdscript", "git_config", "git_rebase", "glsl", "godot_resource", "groovy", "haxe", "hlsl", "http", "hyprlang",
	"kconfig", "less", "linkerscript", "nim", "norg", "nushell", "odin", "purescript", "rescript", "verilog",
}
