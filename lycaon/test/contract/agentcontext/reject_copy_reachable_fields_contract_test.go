package contract

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const toolSchemaDir = "lycaon/config/packs/painted-wolf/platform/tools/schemas"

// backtickedIdent matches argument names and dotted paths.
var backtickedIdent = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)*)`")

// toolFieldIndex is what each tool can carry, read from the shipped schemas.
type toolFieldIndex struct {
	byTool map[string]map[string]bool
	union  map[string]bool
}

func loadToolFieldIndex(t *testing.T) toolFieldIndex {
	t.Helper()
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(contractcheck.RepoRoot(t), toolSchemaDir))
	contractcheck.FailErr(t, "load tool schemas", err)
	idx := toolFieldIndex{
		byTool: map[string]map[string]bool{},
		union:  map[string]bool{},
	}
	for name := range cfg.Tools {
		meta, ok := cfg.ToolMeta(name)
		if !ok {
			continue
		}
		fields := map[string]bool{}
		for _, path := range toolschema.ArgFieldPaths(meta.ArgsSchema) {
			fields[path] = true
			idx.union[path] = true
			// A dotted path also asserts its own head segment.
			if head, _, cut := strings.Cut(path, "."); cut {
				fields[head] = true
				idx.union[head] = true
			}
		}
		idx.byTool[name] = fields
	}
	if len(idx.byTool) == 0 || len(idx.union) == 0 {
		t.Fatal("tool schema index is empty — the walk found no tools or no fields")
	}
	return idx
}

// fieldsFor returns the argument names a tool accepts, as the producer takes them.
func (idx toolFieldIndex) fieldsFor(name string) []string {
	fields := idx.byTool[name]
	out := make([]string, 0, len(fields))
	for field := range fields {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func (idx toolFieldIndex) isTool(name string) bool {
	_, ok := idx.byTool[name]
	return ok
}

// unreachableFieldClaims returns unsupported argument claims.
func (idx toolFieldIndex) unreachableFieldClaims(tool, text string) []string {
	own := idx.byTool[tool]
	named := map[string]bool{}
	var claims []string
	for _, m := range backtickedIdent.FindAllStringSubmatch(text, -1) {
		token := m[1]
		head, _, _ := strings.Cut(token, ".")
		if idx.isTool(head) {
			named[head] = true
			continue
		}
		if !idx.union[token] {
			continue
		}
		claims = append(claims, token)
	}
	var bad []string
	for _, claim := range claims {
		if own[claim] {
			continue
		}
		reachable := false
		for other := range named {
			if idx.byTool[other][claim] {
				reachable = true
				break
			}
		}
		if !reachable {
			bad = append(bad, claim)
		}
	}
	sort.Strings(bad)
	return contractcheck.DedupeStrings(bad)
}

// TestRejectCopyNamesOnlyReachableFields verifies rejection argument claims.
func TestRejectCopyNamesOnlyReachableFields(t *testing.T) {
	t.Parallel()
	idx := loadToolFieldIndex(t)

	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint config", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	formatter := guidance.NewStaticRejectFormatter(cfg)

	codes := make([]string, 0, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	var violations []string
	checked := 0
	for _, code := range codes {
		entry := cfg.HintCodes[code]
		if len(entry.Tools) == 0 {
			continue
		}
		for _, tool := range entry.Tools {
			tool = strings.TrimSpace(tool)
			if !idx.isTool(tool) {
				continue
			}
			checked++
			// Scan the rendered card as one block.
			text := strings.Join(rejectCopyTexts(t, formatter, code, entry, tool), "\n")
			for _, claim := range idx.unreachableFieldClaims(tool, text) {
				violations = append(violations, fmt.Sprintf(
					"%s rendered for %q names `%s`, which %s does not accept",
					code, tool, claim, tool,
				))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no (card, tool) pair was checked — the corpus or the selector read is broken")
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "reject copy names fields the receiving tool cannot carry", contractcheck.DedupeStrings(violations))
}

// rejectCopyTexts returns rendered scenarios and raw copy members.
func rejectCopyTexts(
	t *testing.T,
	formatter *guidance.StaticRejectFormatter,
	code string,
	entry guidance.HintEntry,
	tool string,
) []string {
	t.Helper()
	texts := []string{entry.What, entry.Cause, entry.Why, entry.Fix, entry.Instead}
	for _, sc := range entry.Scenarios {
		vars := guidance.ScenarioVars(entry, sc)
		vars["tool"] = tool
		rendered, err := formatter.Format(code, vars)
		if err != nil {
			// Selector tests report render failures separately.
			continue
		}
		texts = append(texts, rendered)
	}
	return texts
}

// TestCommandSurfaceRejectRoutesStayInsideTheirSelector verifies tool routing.
func TestCommandSurfaceRejectRoutesStayInsideTheirSelector(t *testing.T) {
	t.Parallel()
	idx := loadToolFieldIndex(t)

	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint config", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(engine))
	formatter := guidance.NewStaticRejectFormatter(cfg)

	surfaceErrors := map[string]error{
		"unquoted pipe":     commandsurface.ErrSequenceUnsupported,
		"missing argv":      commandsurface.ErrArgvRequired,
		"argv conflict":     commandsurface.ErrArgvConflict,
		"cd as a program":   commandsurface.ErrDirectoryChangeCommand,
		"stdin on sequence": commandsurface.ErrStdinWithSequence,
	}

	toolNames := make([]string, 0, len(idx.byTool))
	for name := range idx.byTool {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)

	var violations []string
	routed := 0
	for _, tool := range toolNames {
		for label, surfaceErr := range surfaceErrors {
			rej := tools.CommandSurfaceObservation(tool, "implement",
				"go test ./... | head", map[string]any{"command": "go test ./... | head"}, idx.fieldsFor(tool), surfaceErr)
			if rej == nil {
				continue
			}
			routed++
			entry, ok := cfg.HintCodes[rej.Code]
			if !ok {
				violations = append(violations, fmt.Sprintf(
					"%s routes %q to unknown code %s", label, tool, rej.Code))
				continue
			}
			if !selectorCovers(entry, tool) {
				violations = append(violations, fmt.Sprintf(
					"%s routes %q to %s, whose selector is %v", label, tool, rej.Code, entry.Tools))
				continue
			}
			// The route is in-selector, so the card must actually render for it.
			data := rej.Data
			if data == nil {
				data = map[string]any{}
			}
			data["tool"] = tool
			if _, err := formatter.Format(rej.Code, data); err != nil {
				violations = append(violations, fmt.Sprintf(
					"%s routes %q to %s, which does not render: %v", label, tool, rej.Code, err))
			}
		}
	}
	if routed == 0 {
		t.Fatal("no command-surface reject was produced — the producer or the error set is broken")
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "command-surface reject routes outrun their card's selector", contractcheck.DedupeStrings(violations))
}

func selectorCovers(entry guidance.HintEntry, tool string) bool {
	if len(entry.Tools) == 0 {
		return true
	}
	for _, allowed := range entry.Tools {
		if strings.EqualFold(strings.TrimSpace(allowed), tool) {
			return true
		}
	}
	return false
}
