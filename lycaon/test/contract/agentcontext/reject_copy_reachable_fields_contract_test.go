package contract

import (
	"github.com/lycaon/lycaon/internal/toolrejection"

	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
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

func (idx toolFieldIndex) isTool(name string) bool {
	_, ok := idx.byTool[name]
	return ok
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
			rej := toolrejection.CommandSurfaceObservation(tool, "implement",
				"go test ./... | head", map[string]any{"command": "go test ./... | head"}, surfaceErr)
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
