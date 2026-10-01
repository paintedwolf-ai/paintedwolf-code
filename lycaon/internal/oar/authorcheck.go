package oar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance"
)

// Author-check result codes are stable rule-test outcomes.
const (
	RuleCheckOK               = ""
	RuleCheckLoadError        = "load_error"
	RuleCheckSchemaInvalid    = "schema_invalid"
	RuleCheckConditionInvalid = "condition_invalid"
	RuleCheckScenarioMismatch = "scenario_mismatch"
)

// RuleCheckResult is one rule's author-check outcome. Rules and their
// conformance fixtures are checked together: a rule that loads but whose own
// fixtures disagree with it is still a failing rule.
type RuleCheckResult struct {
	RuleID    string `json:"rule_id"`
	Path      string `json:"path"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	Scenarios int    `json:"scenarios"`
}

// OK reports whether the rule passed every check.
func (r RuleCheckResult) OK() bool { return r.Code == RuleCheckOK }

// RuleCheckOptions configures CheckRuleFiles.
type RuleCheckOptions struct {
	// SchemaDir holds oar.schema.json and its siblings.
	SchemaDir string
	// AnchorCatalogPath is the host anchor catalog a rule must use.
	AnchorCatalogPath string
}

// CheckRuleFiles validates rule documents and runs the conformance fixtures
// that sit beside them. Paths may be rule files, directories of rules (a pack's
// policy/ tree), or conformance fixture JSON. A directory is also searched for
// a sibling conformance/ directory of fixtures.
//
// The result set is one row per rule plus one row per fixture that names a
// rule no checked file provided.
func CheckRuleFiles(paths []string, opts RuleCheckOptions) ([]RuleCheckResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no rule paths given")
	}
	// Install the catalog before loading rules.
	if catalog := strings.TrimSpace(opts.AnchorCatalogPath); catalog != "" {
		if err := anchorcatalog.InstallFile(catalog); err != nil {
			return nil, fmt.Errorf("install anchor catalog: %w", err)
		}
	} else if err := anchorcatalog.InstallBundled(); err != nil {
		return nil, fmt.Errorf("install bundled anchor catalog: %w", err)
	}
	if err := InstallCapabilityBundled(strings.TrimSpace(opts.SchemaDir)); err != nil {
		return nil, err
	}
	ruleDirs, fixtureFiles, err := classifyRulePaths(paths)
	if err != nil {
		return nil, err
	}

	loader, err := NewLoader(opts.SchemaDir)
	if err != nil {
		return nil, fmt.Errorf("oar loader: %w", err)
	}
	runner, err := NewConformanceRunner(opts.SchemaDir)
	if err != nil {
		return nil, fmt.Errorf("conformance runner: %w", err)
	}

	fixturesByRule := map[string][]ConformanceFixture{}
	var orphanFixtures []ConformanceFixture
	for _, path := range fixtureFiles {
		fxs, err := loadFixtureFile(path)
		if err != nil {
			return nil, err
		}
		for _, fx := range fxs {
			id := fixtureRuleID(fx)
			if id == "" {
				orphanFixtures = append(orphanFixtures, fx)
				continue
			}
			fixturesByRule[id] = append(fixturesByRule[id], fx)
		}
	}

	var out []RuleCheckResult
	checked := map[string]bool{}
	for _, dir := range ruleDirs {
		rs, err := loader.LoadDir(extpacks.OnDisk(dir.dir))
		if err != nil {
			out = append(out, loadErrorResults(dir, err)...)
			continue
		}
		for _, rule := range rs.All() {
			if len(dir.only) > 0 && !dir.only[rule.ID] {
				continue
			}
			checked[rule.ID] = true
			res := RuleCheckResult{RuleID: rule.ID, Path: ruleSourcePath(rule, dir.label)}
			for _, fx := range fixturesByRule[rule.ID] {
				res.Scenarios++
				if err := runner.RunFixture(fx); err != nil {
					res.Code = RuleCheckScenarioMismatch
					res.Detail = fmt.Sprintf("%s: %v", fixtureName(fx), err)
					break
				}
			}
			// x-paintedwolf-scenarios are the copy contract: render this Code with
			// these vars and it says these things.
			if res.Code == "" {
				rendered, mismatch := checkRenderScenarios(rule)
				res.Scenarios += rendered
				if mismatch != "" {
					res.Code = RuleCheckScenarioMismatch
					res.Detail = mismatch
				}
			}
			out = append(out, res)
		}
	}

	for id, fxs := range fixturesByRule {
		if checked[id] {
			continue
		}
		for _, fx := range fxs {
			res := RuleCheckResult{RuleID: id, Path: fixtureName(fx), Scenarios: 1}
			if err := runner.RunFixture(fx); err != nil {
				res.Code = RuleCheckScenarioMismatch
				res.Detail = err.Error()
			}
			out = append(out, res)
		}
	}
	for _, fx := range orphanFixtures {
		res := RuleCheckResult{Path: fixtureName(fx), Scenarios: 1}
		if err := runner.RunFixture(fx); err != nil {
			res.Code = RuleCheckScenarioMismatch
			res.Detail = err.Error()
		}
		out = append(out, res)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// ruleCheckDir is one directory of rule documents, optionally narrowed to the
// single file the author named.
type ruleCheckDir struct {
	dir   string
	label string
	only  map[string]bool
}

func classifyRulePaths(paths []string) ([]ruleCheckDir, []string, error) {
	var dirs []ruleCheckDir
	var fixtures []string
	seenDir := map[string]bool{}
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		if info.IsDir() {
			if !seenDir[path] {
				seenDir[path] = true
				dirs = append(dirs, ruleCheckDir{dir: path, label: path})
			}
			fixtures = append(fixtures, discoverFixtureFiles(path)...)
			continue
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json":
			fixtures = append(fixtures, path)
		case ".yaml", ".yml":
			dir := filepath.Dir(path)
			code := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			dirs = append(dirs, ruleCheckDir{dir: dir, label: path, only: map[string]bool{code: true}})
			fixtures = append(fixtures, discoverFixtureFiles(dir)...)
		default:
			return nil, nil, fmt.Errorf("%s: not a rule document or conformance fixture", path)
		}
	}
	if len(dirs) == 0 && len(fixtures) == 0 {
		return nil, nil, fmt.Errorf("no rule documents or fixtures found")
	}
	return dirs, dedupeStrings(fixtures), nil
}

// discoverFixtureFiles finds the conformance fixtures a pack ships beside its
// rules: <dir>/conformance and <dir>/../conformance.
func discoverFixtureFiles(dir string) []string {
	var out []string
	for _, candidate := range []string{
		filepath.Join(dir, "conformance"),
		filepath.Join(filepath.Dir(dir), "conformance"),
	} {
		ents, err := os.ReadDir(candidate)
		if err != nil {
			continue
		}
		for _, ent := range ents {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
				continue
			}
			out = append(out, filepath.Join(candidate, ent.Name()))
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// loadErrorResults turns a directory load failure into one row per broken rule
// document. The loader reports every bad file in one error; rows are narrowed
// to the files the author named.
func loadErrorResults(dir ruleCheckDir, err error) []RuleCheckResult {
	var out []RuleCheckResult
	for _, entry := range splitLoadErrorEntries(err.Error()) {
		path, detail := entry, entry
		if i := strings.Index(entry, ": "); i > 0 {
			path, detail = entry[:i], entry[i+2:]
		}
		fields := strings.Fields(path)
		ruleID := ""
		if len(fields) > 1 {
			ruleID = fields[len(fields)-1]
			path = strings.Join(fields[:len(fields)-1], " ")
		}
		if len(dir.only) > 0 && !dir.only[ruleID] && filepath.Clean(path) != filepath.Clean(dir.label) {
			continue
		}
		out = append(out, RuleCheckResult{
			RuleID: ruleID,
			Path:   path,
			Code:   classifyLoadError(detail),
			Detail: detail,
		})
	}
	if len(out) == 0 {
		out = append(out, RuleCheckResult{
			Path:   dir.label,
			Code:   classifyLoadError(err.Error()),
			Detail: err.Error(),
		})
	}
	return out
}

// splitLoadErrorEntries groups a joined load error by document.
func splitLoadErrorEntries(msg string) []string {
	var entries []string
	for _, line := range strings.Split(msg, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "oar load:" {
			continue
		}
		if startsLoadErrorEntry(trimmed) || len(entries) == 0 {
			entries = append(entries, trimmed)
			continue
		}
		entries[len(entries)-1] += "\n" + trimmed
	}
	return entries
}

func startsLoadErrorEntry(line string) bool {
	head := line
	if i := strings.IndexAny(head, " :"); i > 0 {
		head = head[:i]
	}
	ext := strings.ToLower(filepath.Ext(head))
	return ext == ".yaml" || ext == ".yml"
}

const (
	schemaInstanceErrorMarker = "schema instance"
	whenClauseErrorMarker     = "when \""
)

// classifyLoadError maps loader detail to an author-check code.
func classifyLoadError(msg string) string {
	switch {
	case strings.Contains(msg, "oar.schema.json"), strings.Contains(msg, schemaInstanceErrorMarker):
		return RuleCheckSchemaInvalid
	case strings.Contains(msg, whenClauseErrorMarker):
		return RuleCheckConditionInvalid
	default:
		return RuleCheckLoadError
	}
}

// loadFixtureFile reads one conformance fixture file, accepting either a single
// fixture object or a JSON array of them.
func loadFixtureFile(path string) ([]ConformanceFixture, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- author-supplied fixture path
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var list []ConformanceFixture
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for i := range list {
			if strings.TrimSpace(list[i].Name) == "" {
				list[i].Name = fmt.Sprintf("%s[%d]", filepath.Base(path), i)
			}
		}
		return list, nil
	}
	var fx ConformanceFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(fx.Name) == "" {
		fx.Name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	return []ConformanceFixture{fx}, nil
}

// fixtureRuleID reads the rule id out of a fixture's embedded rule document.
func fixtureRuleID(fx ConformanceFixture) string {
	var doc struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(fx.Rule, &doc); err != nil {
		return ""
	}
	return strings.TrimSpace(doc.ID)
}

func fixtureName(fx ConformanceFixture) string {
	if name := strings.TrimSpace(fx.Name); name != "" {
		return name
	}
	return "(unnamed fixture)"
}

func ruleSourcePath(r *Rule, fallback string) string {
	if r != nil && strings.TrimSpace(r.Source) != "" {
		return r.Source
	}
	return fallback
}

// checkRenderScenarios renders the rule's copy for each scenario it declares
// through the host's own reject block, and reports how many ran plus the first
// mismatch.
func checkRenderScenarios(rule *Rule) (ran int, mismatch string) {
	if len(rule.Scenarios) == 0 {
		return 0, ""
	}
	entry := hintEntryForRule(rule)
	formatter := guidance.NewStaticRejectFormatter(&guidance.HintConfig{
		HintCodes: map[string]guidance.HintEntry{rule.ID: entry},
	})
	for _, sc := range rule.Scenarios {
		ran++
		body, err := formatter.Format(rule.ID, guidance.ScenarioVars(entry, sc))
		if err != nil {
			return ran, fmt.Sprintf("scenario %q: %v", sc.ID, err)
		}
		for _, want := range sc.ExpectContains {
			if !strings.Contains(body, want) {
				return ran, fmt.Sprintf("scenario %q: rendered copy does not contain %q", sc.ID, want)
			}
		}
	}
	return ran, ""
}

// hintEntryForRule projects a rule onto the copy fields the formatter reads.
func hintEntryForRule(rule *Rule) guidance.HintEntry {
	return guidance.HintEntry{
		Emit:    rule.Emit,
		Effect:  string(rule.Effect),
		Anchor:  rule.Anchor,
		Tools:   rule.Selector["tool"],
		What:    rule.Copy.What,
		Cause:   rule.Copy.Cause,
		Why:     rule.Copy.Why,
		Fix:     rule.Fix,
		Instead: rule.Copy.Instead,
	}
}
