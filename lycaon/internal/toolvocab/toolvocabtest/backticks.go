package toolvocabtest

import (
	"fmt"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/internal/workercompletionxml"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workflow/verdictcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// BacktickKind classifies an inline backtick span in model-facing host prose.
type BacktickKind string

const (
	BacktickToolOrOperation BacktickKind = "tool_or_operation"
	BacktickCommandForm     BacktickKind = "command_form"
	BacktickSchemaToken     BacktickKind = "schema_token"
	BacktickHandleOrRoot    BacktickKind = "handle_or_root"
	BacktickCodeOrCatalogID BacktickKind = "code_or_catalog_id"
	BacktickSyntaxLiteral   BacktickKind = "syntax_literal"
	BacktickUnclassified    BacktickKind = "unclassified"
)

// BacktickSpan is one extracted inline backtick span and its classification.
type BacktickSpan struct {
	Span   string
	Line   int
	Kind   BacktickKind
	Detail string
}

// BacktickVocab holds the closed protocol catalogs for inline backtick classification.
type BacktickVocab struct {
	catalog       *toolvocab.Catalog
	operations    map[string]bool
	retiredTools  map[string]bool
	commands      map[string]bool
	commandFlags  map[string]bool
	commandSyntax map[string]bool
	schemaItems   map[string]bool
	evidenceKinds map[string]bool
	codes         map[string]bool
	catalogIDs    map[string]bool
	rootLabels    map[string]bool
}

var (
	stockBacktickOnce = sync.OnceValues(func() (*BacktickVocab, error) {
		return NewBacktickVocab(nil)
	})
	screamingCodeRe  = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9*]+)+$`)
	snakeIdentRe     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)+\*?$`)
	camelIdentRe     = regexp.MustCompile(`^[a-z]+[A-Z][A-Za-z0-9]*$`)
	plainWordLitRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*:?$`)
	callSpanShapeRe  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\((.*)\)$`)
	handleSpanRe     = regexp.MustCompile(`^(?:handle:)?(?:\[)?(?:[a-z0-9_-]+:)?([a-z][a-z0-9_]*)#[0-9Nn]*(?:\s+"[^"]*"|\s+[^\]]+)?(?:\])?$`)
	virtualRootRe    = regexp.MustCompile(`^@(<[a-z_]+>|[A-Za-z0-9_-]+)(?:/.*)?$`)
	fileExtLikeRe    = regexp.MustCompile(`^(?:\.[a-z0-9]{1,8}|[A-Za-z0-9_./-]+\.(?:md|go|ts|tsx|js|json|yaml|yml|toml|sql|py|sh|css|html|svg|png|jpg|webp|gif|txt|lock))$`)
	cssOrMathExprRe  = regexp.MustCompile(`^(?:(?:var|rgba?|hsla?|url|calc|clamp|min|max)\(.*\)|[0-9.]+(?:px|rem|em|vh|vw|%|ms|s)|count\(\*\)|.+==.+)$`)
	standardEnvNames = map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
		"TMPDIR": true, "HOME": true, "PATH": true, "USER": true, "PWD": true, "OLDPWD": true,
	}
	commonExternalCLIs = map[string]bool{
		"openssl": true, "docker": true, "podman": true, "python3": true, "python": true,
		"yq": true, "sops": true, "doppler": true, "lsof": true, "screencapture": true,
		"chmod": true, "eval": true, "exec": true, "printf": true, "nl": true, "patch": true,
	}
)

// StockBacktickVocab returns the cached closed backtick vocabulary for stock packs.
func StockBacktickVocab() (*BacktickVocab, error) { return stockBacktickOnce() }

// NewBacktickVocab constructs a closed backtick vocabulary from eff (or stock packs when nil).
func NewBacktickVocab(eff *extpacks.EffectiveCatalog) (*BacktickVocab, error) {
	if eff == nil {
		var err error
		if eff, err = extpacks.CatalogForConsumers(); err != nil {
			return nil, err
		}
	}
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		return nil, err
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, err
	}
	cat, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	if err != nil {
		return nil, err
	}
	v := &BacktickVocab{
		catalog: cat, operations: map[string]bool{}, retiredTools: map[string]bool{},
		commands: map[string]bool{}, commandFlags: map[string]bool{}, commandSyntax: map[string]bool{},
		schemaItems: map[string]bool{}, evidenceKinds: map[string]bool{},
		codes: map[string]bool{}, catalogIDs: map[string]bool{},
		rootLabels: map[string]bool{"scratch": true, "output": true, "label": true, "<label>": true, "main": true, "den": true, "primary": true, "secondary": true, "fixture": true},
	}
	for _, p := range profiles {
		v.catalogIDs[p.ID] = true
	}
	if err := v.populateToolsAndCommands(schemas); err != nil {
		return nil, err
	}
	v.populateCatalogAndEnvelopeIDs(eff, schemas)
	return v, nil
}

func (v *BacktickVocab) populateToolsAndCommands(schemas *toolschema.Config) error {
	manifest, err := nativemanifest.Load()
	if err != nil {
		return err
	}
	for _, op := range manifest.AllOperations() {
		v.operations[strings.TrimSpace(op)] = true
	}
	lang, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return err
	}
	for _, td := range lang.Tools {
		v.retiredTools[td.Tool] = true
		for _, cf := range td.CommandForms {
			v.commands[cf] = true
		}
		if td.ExistingRejectionCode != "" {
			v.codes[td.ExistingRejectionCode] = true
		}
	}
	for _, cmd := range lang.Commands {
		v.commands[cmd.Name] = true
		for _, al := range cmd.Aliases {
			v.commands[al] = true
		}
		for _, form := range cmd.Forms {
			for _, sp := range form.Spellings {
				v.commandFlags[sp] = true
			}
		}
		if cmd.Unsupported.Code != "" {
			v.codes[cmd.Unsupported.Code] = true
		}
	}
	for _, items := range lang.Language.Syntax {
		for _, item := range items {
			v.commandSyntax[item] = true
		}
	}
	for _, op := range []string{"&", "&&", "||", "|", ";", "$!", "$PWD", "$OLDPWD", "CMD --help", "jobs", "cd", "pwd", "ps", "pgrep", "kill", "wait"} {
		v.commandSyntax[op] = true
	}
	for _, diag := range lang.Language.Diagnostics {
		v.codes[diag.ID] = true
	}
	for name := range schemas.Tools {
		if meta, ok := schemas.ToolMeta(name); ok {
			indexSchemaVocabulary(meta.ArgsSchema, v.schemaItems)
		}
	}
	for kind := range evidence.ActiveBinding().KindShapeMap() {
		v.evidenceKinds[kind] = true
	}
	return nil
}

func (v *BacktickVocab) populateCatalogAndEnvelopeIDs(eff *extpacks.EffectiveCatalog, schemas *toolschema.Config) {
	if entries, err := hintregistry.ListEffectiveWithCatalog(eff); err == nil {
		for _, e := range entries {
			v.codes[e.Code] = true
		}
	}
	for _, outcome := range isolation.Outcomes() {
		v.codes[outcome.Code] = true
	}
	if agents, err := agentdef.LoadEffectiveWithCatalog(eff); err == nil {
		for _, a := range agents {
			v.catalogIDs[a.ID] = true
			if a.Spawn.MisconfigureCode != "" {
				v.codes[a.Spawn.MisconfigureCode] = true
			}
		}
	}
	v.catalogIDs["coordinator"] = true
	v.catalogIDs["worker"] = true
	if surfCat, err := surfacecatalog.Load(); err == nil {
		for _, id := range surfCat.SurfaceIDs() {
			v.catalogIDs[id] = true
		}
	}
	v.catalogIDs["observe_"] = true
	if skillsList, err := extpacks.LoadEffectiveSkills(eff); err == nil {
		for _, sk := range skillsList {
			v.catalogIDs[sk.Name] = true
		}
	}
	var baseVerdict map[string]any
	if meta, ok := schemas.ToolMeta("submit_verdict"); ok {
		baseVerdict = meta.ArgsSchema
	}
	if wfReg, err := workflowdef.RegistryFromDirs(""); err == nil {
		for _, m := range wfReg.All() {
			v.catalogIDs[m.ID] = true
			if m.Version != "" {
				v.catalogIDs[m.ID+"@"+m.Version] = true
				v.catalogIDs[m.ID+"@"] = true
			}
			for _, ph := range m.PhaseDefs {
				v.catalogIDs[ph.ID] = true
				for _, g := range ph.Gates {
					v.catalogIDs[g] = true
				}
				for _, tr := range ph.Transitions {
					v.catalogIDs[tr.ID] = true
				}
				if ph.ReviewLoop != nil {
					for f := range ph.ReviewLoop.VerdictSchema {
						v.schemaItems[f] = true
					}
					for st := range ph.ReviewLoop.ClaimStatuses {
						v.schemaItems[st] = true
					}
					if baseVerdict != nil {
						if composed, cErr := verdictcall.Compose(baseVerdict, *ph.ReviewLoop, nil); cErr == nil {
							indexSchemaVocabulary(composed, v.schemaItems)
						}
					}
				}
			}
		}
	}
	for _, cfgPath := range []string{
		"packs/painted-wolf/platform/host/reader-facts.yaml",
		"packs/painted-wolf/platform/host/anchors/catalog.yaml",
		"packs/painted-wolf/platform/host/anchors/oar-profile.yaml",
	} {
		if raw, err := fs.ReadFile(config.Source(), cfgPath); err == nil {
			for _, m := range regexp.MustCompile(`\b[a-z][a-z0-9_]{2,50}\b`).FindAllString(string(raw), -1) {
				v.catalogIDs[m] = true
			}
		}
	}
	collectStructTagNames(v.catalogIDs,
		reflect.TypeFor[workercompletionxml.Doc](), reflect.TypeFor[api.Session](),
		reflect.TypeFor[api.Message](), reflect.TypeFor[api.ToolResult](),
		reflect.TypeFor[api.WorkerResult](), reflect.TypeFor[api.ArtifactListResponse](),
		reflect.TypeFor[api.CoordinatorRunContext](), reflect.TypeFor[surveyreceipt.Receipt](),
		reflect.TypeFor[survey.SummarizeResponse](),
	)
	for _, m := range hostmarker.All() {
		v.catalogIDs[strings.TrimSpace(m.Value)] = true
	}
	for _, lit := range []string{
		string(recall.ResolutionScopeEmpty), string(verification.SourceChanged),
		runstate.ReviewBlockedReason, string(api.WorkerSummaryStatusNeedsDecision),
		string(api.BackgroundProcessTerminationReasonStoppedByUser),
		"evidence_passed:", "evidence_passed:*", "resolved_by:user",
		"invoked:false", "invoked:true", "status:rejected", "Cause:", "Fix:",
	} {
		v.catalogIDs[lit] = true
	}
}

// ExtractInlineBackticks extracts inline backtick spans outside fenced code blocks.
func ExtractInlineBackticks(prose string) []BacktickSpan {
	var out []BacktickSpan
	inFence := ""
	for idx, lineText := range strings.Split(prose, "\n") {
		lineNum := idx + 1
		trimmed := strings.TrimSpace(lineText)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if inFence == "" {
				inFence = marker
			} else if inFence == marker {
				inFence = ""
			}
			continue
		}
		if inFence != "" {
			continue
		}
		out = append(out, scanLineBackticks(lineText, lineNum)...)
	}
	return out
}

// Classify classifies a single inline backtick span against the closed protocol catalogs.
func (v *BacktickVocab) Classify(span string) (BacktickKind, string) {
	s := strings.TrimSpace(span)
	if s == "" {
		return BacktickSyntaxLiteral, "empty"
	}
	if kind, detail, ok := v.classifyHandleOrRoot(s); ok {
		return kind, detail
	}
	if kind, detail, ok := v.classifyToolOrCall(s); ok {
		return kind, detail
	}
	if kind, detail, ok := v.classifyCommand(s); ok {
		return kind, detail
	}
	if kind, detail, ok := v.classifySchema(s); ok {
		return kind, detail
	}
	if kind, detail, ok := v.classifyCodeOrID(s); ok {
		return kind, detail
	}
	if isExplicitSyntaxLiteral(s) {
		return BacktickSyntaxLiteral, "syntax/prose literal"
	}
	return BacktickUnclassified, fmt.Sprintf("unclassified protocol backtick span %q", s)
}

// CheckProse returns all unclassified inline backtick spans in host-authored prose.
func (v *BacktickVocab) CheckProse(sourceID, prose string) []BacktickSpan {
	if isAuthoringMetaSource(sourceID) {
		return nil
	}
	var bad []BacktickSpan
	for _, sp := range ExtractInlineBackticks(prose) {
		sp.Kind, sp.Detail = v.Classify(sp.Span)
		if sp.Kind == BacktickUnclassified {
			bad = append(bad, sp)
		}
	}
	return bad
}

// FormatBacktickDiagnostics formats unclassified backtick spans with source and line numbers.
func FormatBacktickDiagnostics(sourceID string, spans []BacktickSpan) string {
	var lines []string
	for _, sp := range spans {
		lines = append(lines, fmt.Sprintf("%s:%d: %s (`%s`)", sourceID, sp.Line, sp.Detail, sp.Span))
	}
	return strings.Join(lines, "\n")
}

func (v *BacktickVocab) classifyHandleOrRoot(s string) (BacktickKind, string, bool) {
	if s == "#" {
		return BacktickSyntaxLiteral, "markdown hash", true
	}
	if m := handleSpanRe.FindStringSubmatch(s); len(m) == 2 {
		kind := m[1]
		if v.evidenceKinds[kind] || kind == "kind" {
			return BacktickHandleOrRoot, "evidence handle", true
		}
		return BacktickUnclassified, fmt.Sprintf("undeclared evidence handle kind %q", kind), true
	}
	if m := virtualRootRe.FindStringSubmatch(s); len(m) == 2 {
		label := m[1]
		if v.rootLabels[label] || (!strings.Contains(label, "_") && plainWordLitRe.MatchString(label)) {
			return BacktickHandleOrRoot, "virtual path root", true
		}
		return BacktickUnclassified, fmt.Sprintf("undeclared virtual path root %q", "@"+label), true
	}
	if strings.HasPrefix(s, "${overlay_dir}") || strings.HasPrefix(s, "prompt-attachments/") || strings.HasPrefix(s, "tool-output/") {
		return BacktickHandleOrRoot, "host directory root", true
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if strings.HasPrefix(s, "[cite:") || strings.HasPrefix(s, "[verify:") || strings.HasPrefix(s, "[artifact:") ||
			strings.HasPrefix(s, strings.TrimSpace(hostmarker.CompactionBannerOpen)) || strings.Contains(s, ":") || strings.Contains(s, "/") || s == "[]" || len(s) <= 4 {
			return BacktickHandleOrRoot, "citation or bracket marker", true
		}
	}
	return "", "", false
}

func (v *BacktickVocab) classifyToolOrCall(s string) (BacktickKind, string, bool) {
	if v.catalog.KnownName(s) || v.catalog.KnownPattern(s) || v.operations[s] || v.retiredTools[s] {
		return BacktickToolOrOperation, "tool or operation", true
	}
	if cssOrMathExprRe.MatchString(s) {
		return BacktickSyntaxLiteral, "css/math expression", true
	}
	if m := callSpanShapeRe.FindStringSubmatch(s); len(m) == 3 {
		callee := m[1]
		if v.catalog.KnownName(callee) || v.operations[callee] || v.retiredTools[callee] || v.commands[callee] {
			return BacktickToolOrOperation, "tool call example", true
		}
		return BacktickUnclassified, fmt.Sprintf("unknown callable %q in call span", callee), true
	}
	return "", "", false
}

func (v *BacktickVocab) classifyCommand(s string) (BacktickKind, string, bool) {
	if v.commands[s] || v.commandFlags[s] || v.commandSyntax[s] || commonExternalCLIs[s] {
		return BacktickCommandForm, "command or flag", true
	}
	if strings.HasPrefix(s, "-") && len(s) >= 2 && !strings.HasPrefix(s, "- [") {
		firstWord, _, _ := strings.Cut(s, " ")
		flagName, _, _ := strings.Cut(firstWord, "=")
		if v.commandFlags[flagName] || strings.HasPrefix(flagName, "--") || len(flagName) == 2 {
			return BacktickCommandForm, "command option form", true
		}
	}
	fields := strings.Fields(s)
	if len(fields) >= 2 {
		head := fields[0]
		if strings.Contains(head, "=") && !strings.Contains(head, ":") {
			head = fields[1]
		}
		if v.commands[head] || v.commandSyntax[head] || commonExternalCLIs[head] || head == "cmd" {
			return BacktickCommandForm, "command invocation", true
		}
	}
	return "", "", false
}

func (v *BacktickVocab) classifySchema(s string) (BacktickKind, string, bool) {
	norm := strings.TrimSuffix(normalizeSchemaPath(s), ":")
	if v.schemaItems[norm] {
		return BacktickSchemaToken, "schema path or enum", true
	}
	if head, rest, hasDot := strings.Cut(norm, "."); hasDot && !fileExtLikeRe.MatchString(s) {
		p, _, _, _ := splitPathAndOptionalValue(rest)
		if (v.catalog.KnownName(head) || v.operations[head] || v.retiredTools[head]) && v.schemaItems[p] {
			return BacktickSchemaToken, "tool.field path", true
		}
		if v.catalogIDs[head] && (v.schemaItems[p] || v.catalogIDs[p] || v.catalogIDs[lastDotPart(p)]) {
			return BacktickCodeOrCatalogID, "envelope dotted field", true
		}
	}
	if k, _, hasSep := splitKeyValueTopLevel(s); hasSep {
		nk := normalizeSchemaPath(k)
		if head, sub, hasDot := strings.Cut(nk, "."); hasDot && v.catalog.KnownName(head) && v.schemaItems[sub] {
			return BacktickSchemaToken, "tool.field assignment", true
		}
		if v.schemaItems[nk] || v.catalogIDs[nk] {
			return BacktickSchemaToken, "schema or envelope assignment", true
		}
	}
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		if m, _, _, ok := parseObjectLiteral(s); ok && len(m) > 0 {
			allKnown := true
			for k := range m {
				if !v.schemaItems[k] && !v.catalogIDs[k] {
					allKnown = false
					break
				}
			}
			if allKnown {
				return BacktickSchemaToken, "schema object literal", true
			}
		}
	}
	return "", "", false
}

func (v *BacktickVocab) classifyCodeOrID(s string) (BacktickKind, string, bool) {
	if after, ok := strings.CutPrefix(s, hostmarker.CodeLine); ok && strings.TrimSpace(after) != "" {
		code := strings.TrimSpace(after)
		if v.matchesDeclaredCode(code) {
			return BacktickCodeOrCatalogID, "rejection code", true
		}
		return BacktickUnclassified, fmt.Sprintf("undeclared rejection code %q", code), true
	}
	if v.matchesDeclaredCode(s) || standardEnvNames[s] {
		return BacktickCodeOrCatalogID, "declared code or env", true
	}
	if screamingCodeRe.MatchString(s) {
		return BacktickUnclassified, fmt.Sprintf("undeclared code %q", s), true
	}
	trimmedStar := strings.TrimSuffix(s, "*")
	if v.catalogIDs[s] || v.catalogIDs[trimmedStar] || v.catalogIDs[strings.TrimSuffix(s, ":")] {
		return BacktickCodeOrCatalogID, "catalog identifier", true
	}
	return "", "", false
}

func (v *BacktickVocab) matchesDeclaredCode(code string) bool {
	if v.codes[code] {
		return true
	}
	if prefix, ok := strings.CutSuffix(code, "*"); ok && prefix != "" {
		for c := range v.codes {
			if strings.HasPrefix(c, prefix) {
				return true
			}
		}
	}
	return false
}

func isExplicitSyntaxLiteral(s string) bool {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "file://") ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, ")") && strings.Contains(s, "](")) {
		return true
	}
	if snakeIdentRe.MatchString(s) || camelIdentRe.MatchString(s) || screamingCodeRe.MatchString(s) {
		return false
	}
	if identPathRe.MatchString(s) && strings.Contains(s, ".") && !fileExtLikeRe.MatchString(s) {
		return false
	}
	if k, _, hasSep := splitKeyValueTopLevel(s); hasSep && (snakeIdentRe.MatchString(k) || identPathRe.MatchString(k)) {
		return false
	}
	if strings.HasPrefix(s, "#") || strings.HasPrefix(s, "- [") || strings.HasPrefix(s, "<") ||
		strings.HasPrefix(s, "\"") || strings.HasPrefix(s, "'") || fileExtLikeRe.MatchString(s) ||
		strings.Contains(s, "/") || strings.Contains(s, ":") && strings.Contains(s, " ") {
		return true
	}
	if plainWordLitRe.MatchString(s) {
		return true
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' {
			return strings.Contains(s, " ") && !strings.Contains(s, "_") && !strings.Contains(s, "(")
		}
	}
	return true
}

func indexSchemaVocabulary(schema map[string]any, dst map[string]bool) {
	for _, p := range append(toolschema.ArgFieldPaths(schema), SchemaPropertyPaths(schema)...) {
		dst[p] = true
		for _, seg := range strings.Split(p, ".") {
			dst[seg] = true
		}
	}
	for _, lits := range SchemaEnums(schema) {
		for _, lit := range lits {
			dst[lit] = true
		}
	}
	walkSchemaDefaults(schema, dst, 0)
}

func walkSchemaDefaults(schema map[string]any, dst map[string]bool, depth int) {
	if schema == nil || depth > 8 {
		return
	}
	if def, ok := schema["default"].(string); ok && def != "" {
		dst[def] = true
	}
	if c, ok := schema["const"].(string); ok && c != "" {
		dst[c] = true
	}
	if items, ok := schema["items"].(map[string]any); ok {
		walkSchemaDefaults(items, dst, depth+1)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, raw := range props {
		if child, ok := raw.(map[string]any); ok {
			walkSchemaDefaults(child, dst, depth+1)
		}
	}
}

func collectStructTagNames(dst map[string]bool, rootTypes ...reflect.Type) {
	visited := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for t != nil && (t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			t = t.Elem()
		}
		if t == nil || t.Kind() != reflect.Struct || visited[t] {
			return
		}
		visited[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			for _, tagKey := range []string{"json", "xml"} {
				if raw := f.Tag.Get(tagKey); raw != "" {
					name, _, _ := strings.Cut(raw, ",")
					if name != "" && name != "-" {
						dst[name] = true
					}
				}
			}
			walk(f.Type)
		}
	}
	for _, rt := range rootTypes {
		walk(rt)
	}
}

func scanLineBackticks(line string, lineNum int) []BacktickSpan {
	var out []BacktickSpan
	i := 0
	for i < len(line) {
		if line[i] != '`' {
			i++
			continue
		}
		ticks := 1
		for i+ticks < len(line) && line[i+ticks] == '`' {
			ticks++
		}
		start := i + ticks
		closing := strings.Repeat("`", ticks)
		relEnd := strings.Index(line[start:], closing)
		if relEnd < 0 {
			break
		}
		span := strings.TrimSpace(line[start : start+relEnd])
		if span != "" {
			out = append(out, BacktickSpan{Span: span, Line: lineNum})
		}
		i = start + relEnd + ticks
	}
	return out
}

func isAuthoringMetaSource(sourceID string) bool {
	return strings.Contains(sourceID, "skills/write-a-skill") ||
		strings.Contains(sourceID, "skills/write-prompt-override") ||
		strings.Contains(sourceID, "skills/write-extension-pack") ||
		strings.Contains(sourceID, "skills/write-workflow") ||
		strings.Contains(sourceID, "skills/write-policy-rule")
}

func lastDotPart(p string) string {
	if idx := strings.LastIndexByte(p, '.'); idx >= 0 {
		return p[idx+1:]
	}
	return p
}
