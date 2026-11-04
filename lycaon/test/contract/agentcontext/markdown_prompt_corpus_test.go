package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// promptAudience says who reads an output, which decides what it may teach.
type promptAudience int

const (
	audienceCoordinator promptAudience = iota
	// audienceWorker keeps the typed citation fields of complete_leg.
	audienceWorker
	// audienceTypedTool is copy or schema that belongs to one typed tool.
	audienceTypedTool
)

type renderedOutput struct {
	name     string
	audience promptAudience
	text     string
	// standing outputs reach the model on every turn rather than in feedback.
	standing bool
	// workflowVersioned guidance is a workflow's phase copy, which changes only with
	// that workflow's version.
	workflowVersioned bool
}

// typedCitationTools take citation fields as arguments, and those stay.
var typedCitationTools = []string{"complete_leg", "submit_verdict", "record_finding", "ask_user", "request_decision"}

func namesTypedCitationTool(text string) bool {
	return slices.ContainsFunc(typedCitationTools, func(tool string) bool { return strings.Contains(text, tool) })
}

var pongoNodeRE = regexp.MustCompile(`(?s)\{#.*?#\}|\{\{.*?\}\}|\{%-?\s*(.*?)\s*-?%\}`)

var pongoQuotedRE = regexp.MustCompile(`"([^"]*)"`)

var workflowRenderRE = regexp.MustCompile(`(?m)^\s*render:\s*(\S+)`)

// flattenBranches keeps the text of every side of every conditional, with includes
// inlined, so a fallback no fixture reaches is still read. Only the audience split
// picks a side: coordinator bytes must not count as worker text, or the reverse.
func flattenBranches(t *testing.T, source string, isCoordinator bool) string {
	t.Helper()
	var out strings.Builder
	type frame struct{ gate, active bool }
	var stack []frame
	emitting := func() bool {
		return !slices.ContainsFunc(stack, func(f frame) bool { return f.gate && !f.active })
	}
	offset := 0
	for _, loc := range pongoNodeRE.FindAllStringSubmatchIndex(source, -1) {
		if emitting() {
			out.WriteString(source[offset:loc[0]])
		}
		offset = loc[1]
		if loc[2] < 0 {
			continue
		}
		tag := source[loc[2]:loc[3]]
		name, rest, _ := strings.Cut(tag, " ")
		rest = strings.TrimSpace(rest)
		switch name {
		case "if":
			switch rest {
			case "is_coordinator":
				stack = append(stack, frame{gate: true, active: isCoordinator})
			case "not is_coordinator":
				stack = append(stack, frame{gate: true, active: !isCoordinator})
			default:
				stack = append(stack, frame{})
			}
		case "elif", "else":
			if top := len(stack) - 1; top >= 0 && stack[top].gate {
				stack[top].active = !stack[top].active
			}
		case "endif":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case "set":
			if emitting() {
				for _, literal := range pongoQuotedRE.FindAllStringSubmatch(rest, -1) {
					out.WriteString(literal[1] + "\n")
				}
			}
		case "include":
			if literal := pongoQuotedRE.FindStringSubmatch(rest); literal != nil && emitting() {
				raw, _, err := prompts.DefaultBundledLayout().ReadBundled(literal[1])
				contractcheck.FailErr(t, "read include "+literal[1], err)
				out.WriteString(flattenBranches(t, string(raw), isCoordinator))
			}
		}
	}
	out.WriteString(source[offset:])
	return out.String()
}

// modelFacingOutputs is everything a model reads from the catalog: real renders
// of each surface, every side of every template, every policy, each tool schema,
// the authority notice, and a projected transcript.
func modelFacingOutputs(t *testing.T) []renderedOutput {
	t.Helper()
	contractcheck.ActivateStockCatalog(t)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	outputs := coordinatorSurfaceOutputs(t)
	outputs = append(outputs, templateBranchOutputs(t)...)
	outputs = append(outputs, policyOutputs(t)...)
	outputs = append(outputs, toolSchemaOutputs(t)...)
	outputs = append(outputs, authorityAndProjectionOutputs()...)
	return outputs
}

func coordinatorSurfaceIDs(t *testing.T) (surfacecatalog.Catalog, []string) {
	t.Helper()
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load surface catalog", err)
	ids := catalog.SurfaceIDs()
	if len(ids) == 0 {
		t.Fatal("the surface catalog is empty")
	}
	return catalog, ids
}

func coordinatorSurfaceOutputs(t *testing.T) []renderedOutput {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	_, ids := coordinatorSurfaceIDs(t)
	var out []renderedOutput
	for _, id := range ids {
		for _, posture := range []api.SessionPosture{api.SessionPostureBuild, api.SessionPostureVet} {
			text := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{},
				&api.Session{Posture: posture}, surface.HostLoopWakeSentinel, nil, id)
			out = append(out, renderedOutput{name: "surface " + id + "/" + string(posture), audience: audienceCoordinator, text: text, standing: true})
		}
	}
	return out
}

// templateBranchOutputs flattens every shipped template. An archive is sealed and
// is checked by its own contract. A binding's selector names who reads a guidance
// template; the prompts directory names its persona. Partials and archetypes are
// read through what includes them, which is what gives them an audience.
func templateBranchOutputs(t *testing.T) []renderedOutput {
	t.Helper()
	workerRenders := map[string]bool{}
	for _, binding := range catalogfixture.LoadInformBindings(t).AllInform() {
		if binding != nil && binding.Selector.Surface == "worker" {
			workerRenders[binding.Render] = true
		}
	}
	root := contractcheck.RepoRoot(t)
	workflowRenders := map[string]bool{}
	manifests, err := filepath.Glob(filepath.Join(root, packRoot, "*", "workflows", "*", "workflow.yaml"))
	contractcheck.FailErr(t, "find workflows", err)
	for _, manifest := range manifests {
		raw, readErr := os.ReadFile(manifest)
		contractcheck.FailErr(t, "read "+manifest, readErr)
		for _, render := range workflowRenderRE.FindAllStringSubmatch(string(raw), -1) {
			workflowRenders[render[1]] = true
		}
	}
	var out []renderedOutput
	err = filepath.WalkDir(filepath.Join(root, packRoot), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") || strings.Contains(path, string(filepath.Separator)+"archive"+string(filepath.Separator)) {
			return err
		}
		if dir := filepath.Base(filepath.Dir(path)); dir == "partials" || dir == "archetypes" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		audience := templateAudience(path, workerRenders)
		out = append(out, renderedOutput{
			name:     "template " + filepath.ToSlash(rel),
			audience: audience,
			text:     flattenBranches(t, string(raw), audience == audienceCoordinator),
			standing: templateIsStanding(path),

			workflowVersioned: workflowRenders[strings.TrimSuffix(filepath.Base(path), ".md")],
		})
		return nil
	})
	contractcheck.FailErr(t, "walk pack templates", err)
	return out
}

func templateAudience(path string, workerRenders map[string]bool) promptAudience {
	base := strings.TrimSuffix(filepath.Base(path), ".md")
	if strings.HasSuffix(filepath.ToSlash(filepath.Dir(path)), "/agents/prompts") && !strings.HasPrefix(base, "coordinator-") {
		return audienceWorker
	}
	if workerRenders[base] {
		return audienceWorker
	}
	return audienceCoordinator
}

func templateIsStanding(path string) bool {
	dir := filepath.ToSlash(filepath.Dir(path))
	return strings.HasSuffix(dir, "/agents/prompts") || strings.HasSuffix(dir, "/shared/units")
}

func policyOutputs(t *testing.T) []renderedOutput {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hints", err)
	loader, err := oar.NewLoader(filepath.Join(contractcheck.RepoRoot(t), "schemas"))
	contractcheck.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load policy", err)
	formatter := guidance.NewStaticRejectFormatter(cfg)
	var out []renderedOutput
	for code, entry := range cfg.HintCodes {
		audience := audienceCoordinator
		if rule, ok := rules.Get(code); ok && strings.Contains(rule.When, `paintedwolf.profile == "worker"`) {
			audience = audienceWorker
		}
		copyText := strings.Join([]string{entry.What, entry.Cause, entry.Why, entry.Fix, entry.Instead}, "\n")
		if slices.ContainsFunc(entry.Tools, func(tool string) bool { return slices.Contains(typedCitationTools, tool) }) {
			audience = audienceTypedTool
		}
		// The refusal of a typed tool's own call describes that call's arguments.
		if rule, ok := rules.Get(code); ok && rule.Anchor == "tool.rejected" &&
			(namesTypedCitationTool(copyText) || slices.Contains(rule.Requires.Facts, "paintedwolf.review_expected_call")) {
			audience = audienceTypedTool
		}
		out = append(out, renderedOutput{name: "policy " + code, audience: audience, text: flattenBranches(t, copyText, audience == audienceCoordinator)})
		if guidance.CopyOnlyEmit(entry.Emit) {
			continue
		}
		for _, scenario := range entry.Scenarios {
			body, err := formatter.Format(code, guidance.ScenarioVars(entry, scenario))
			contractcheck.FailErr(t, "render "+code+"/"+scenario.ID, err)
			out = append(out, renderedOutput{name: "policy " + code + "/" + scenario.ID, audience: audience, text: body})
		}
	}
	return out
}

func toolSchemaOutputs(t *testing.T) []renderedOutput {
	t.Helper()
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(contractcheck.RepoRoot(t), toolSchemaDir))
	contractcheck.FailErr(t, "load tool schemas", err)
	names := make([]string, 0, len(cfg.Tools))
	for name := range cfg.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []renderedOutput
	for _, name := range names {
		meta, ok := cfg.ToolMeta(name)
		if !ok {
			continue
		}
		raw, err := json.Marshal(meta)
		contractcheck.FailErr(t, "marshal schema "+name, err)
		audience := audienceCoordinator
		if slices.Contains(typedCitationTools, name) {
			audience = audienceTypedTool
		}
		out = append(out, renderedOutput{name: "tool schema " + name, audience: audience, text: string(raw), standing: true})
	}
	return out
}

func authorityAndProjectionOutputs() []renderedOutput {
	publisherFixture := []api.Message{
		{ID: "u", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Inspect it."},
		{ID: "carrier", Role: api.MessageRoleAssistant, Content: "Checking the entry point.", ToolCalls: []api.ToolCall{{ID: "r1", Name: "read"}}},
		{ID: api.MidTurnNoteID("carrier"), Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Checking the entry point."},
		{ID: "withheld", Role: api.MessageRoleAssistant, Content: "The cause is the timeout.", ToolCalls: []api.ToolCall{{ID: "r2", Name: "read"}}},
	}
	var projected strings.Builder
	for _, message := range transcript.ProjectFor(transcript.AudiencePublisher, publisherFixture) {
		projected.WriteString(message.Content + "\n")
	}
	return []renderedOutput{
		{name: "authority notice, publisher", audience: audienceCoordinator, text: transcript.ContentAuthorityNotice(transcript.AudiencePublisher), standing: true},
		{name: "authority notice, private", audience: audienceWorker, text: transcript.ContentAuthorityNotice(transcript.AudiencePrivate), standing: true},
		{name: "projected transcript with published and withheld carriers", audience: audienceCoordinator, text: projected.String(), standing: true},
	}
}
