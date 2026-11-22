package toolvocabtest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
)

// EnumKey identifies one string enum value at a dot-joined schema path on a tool.
type EnumKey struct {
	Tool, Path, Literal string
}

// ProjectedSchemaSnapshot records projected tool schemas for one host state.
type ProjectedSchemaSnapshot struct {
	StateID string
	Schemas map[string]map[string]any
}

// PrunableSchemaIndex holds schema paths and enum values that production
// projections remove or narrow in at least one host state.
type PrunableSchemaIndex struct {
	prunedPathsByTool map[string]map[string]bool
	prunedPathOwners  map[string][]string
	allPathOwners     map[string][]string
	prunedEnums       map[EnumKey]bool
	baseSchemas       map[string]map[string]any
}

var stockPrunableOnce = sync.OnceValues(func() (*PrunableSchemaIndex, error) {
	return DiscoverPrunableSchemaIndex(context.Background(), nil)
})

// StockPrunableSchemaIndex returns the cached prunable schema index for stock packs.
func StockPrunableSchemaIndex() (*PrunableSchemaIndex, error) { return stockPrunableOnce() }

// IsPrunablePath reports whether path disappears from tool in at least one state.
func (idx *PrunableSchemaIndex) IsPrunablePath(tool, path string) bool {
	return idx != nil && idx.prunedPathsByTool[strings.TrimSpace(tool)][normalizeSchemaPath(path)]
}

// PrunablePaths returns the sorted prunable property paths for tool.
func (idx *PrunableSchemaIndex) PrunablePaths(tool string) []string {
	if idx == nil {
		return nil
	}
	return sortedTrueKeys(idx.prunedPathsByTool[strings.TrimSpace(tool)])
}

// PrunableOwners returns the sorted tools where path is pruned in at least one state.
func (idx *PrunableSchemaIndex) PrunableOwners(path string) []string {
	if idx == nil {
		return nil
	}
	return slices.Clone(idx.prunedPathOwners[normalizeSchemaPath(path)])
}

// AllOwners returns the sorted callable tools whose base schema declares path.
func (idx *PrunableSchemaIndex) AllOwners(path string) []string {
	if idx == nil {
		return nil
	}
	return slices.Clone(idx.allPathOwners[normalizeSchemaPath(path)])
}

// IsPrunableEnum reports whether literal at path on tool is narrowed in at least one state.
func (idx *PrunableSchemaIndex) IsPrunableEnum(tool, path, literal string) bool {
	return idx != nil && idx.prunedEnums[EnumKey{Tool: strings.TrimSpace(tool), Path: normalizeSchemaPath(path), Literal: literal}]
}

// PrunableEnums returns sorted narrowed enum literals for tool at path.
func (idx *PrunableSchemaIndex) PrunableEnums(tool, path string) []string {
	if idx == nil {
		return nil
	}
	tool, path = strings.TrimSpace(tool), normalizeSchemaPath(path)
	var out []string
	for k, ok := range idx.prunedEnums {
		if ok && k.Tool == tool && k.Path == path {
			out = append(out, k.Literal)
		}
	}
	sort.Strings(out)
	return out
}

// AllPrunableEnums returns all narrowed (tool, path, literal) tuples sorted.
func (idx *PrunableSchemaIndex) AllPrunableEnums() []EnumKey {
	if idx == nil {
		return nil
	}
	out := make([]EnumKey, 0, len(idx.prunedEnums))
	for k, ok := range idx.prunedEnums {
		if ok {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b EnumKey) int {
		if c := strings.Compare(a.Tool, b.Tool); c != 0 {
			return c
		}
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Literal, b.Literal)
	})
	return out
}

// BaseSchema returns the base provider-projected ArgsSchema for tool.
func (idx *PrunableSchemaIndex) BaseSchema(tool string) (map[string]any, bool) {
	if idx == nil {
		return nil, false
	}
	s, ok := idx.baseSchemas[strings.TrimSpace(tool)]
	return s, ok
}

// SchemaPropertyPaths returns all accepted dot-joined property paths in schema.
func SchemaPropertyPaths(schema map[string]any) []string {
	seen := map[string]bool{}
	walkSchemaPathsAndEnums(schema, "", seen, nil, 0)
	return sortedTrueKeys(seen)
}

// SchemaEnums returns sorted string enum values keyed by dot-joined property path.
func SchemaEnums(schema map[string]any) map[string][]string {
	raw := map[string]map[string]bool{}
	walkSchemaPathsAndEnums(schema, "", nil, raw, 0)
	out := make(map[string][]string, len(raw))
	for p, vals := range raw {
		out[p] = sortedTrueKeys(vals)
	}
	return out
}

func walkSchemaPathsAndEnums(schema map[string]any, prefix string, paths map[string]bool, enums map[string]map[string]bool, depth int) {
	if schema == nil || depth > 8 {
		return
	}
	if enums != nil && prefix != "" {
		for _, lit := range extractStringEnumSlice(schema["enum"]) {
			addOwner(enums, prefix, lit)
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		walkSchemaPathsAndEnums(items, prefix, paths, enums, depth+1)
	}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		p := name
		if prefix != "" {
			p = prefix + "." + name
		}
		if paths != nil {
			paths[p] = true
		}
		if child, ok := raw.(map[string]any); ok {
			walkSchemaPathsAndEnums(child, p, paths, enums, depth+1)
		}
	}
	for _, comb := range []string{"oneOf", "anyOf", "allOf"} {
		branches, _ := schema[comb].([]any)
		for _, item := range branches {
			if br, ok := item.(map[string]any); ok {
				walkSchemaPathsAndEnums(br, prefix, paths, enums, depth+1)
			}
		}
	}
}

// DiffSchemaProjections compares base tool schemas against projected state snapshots.
func DiffSchemaProjections(base *toolschema.Config, snapshots []ProjectedSchemaSnapshot) *PrunableSchemaIndex {
	idx := &PrunableSchemaIndex{
		prunedPathsByTool: map[string]map[string]bool{}, prunedPathOwners: map[string][]string{},
		allPathOwners: map[string][]string{}, prunedEnums: map[EnumKey]bool{}, baseSchemas: map[string]map[string]any{},
	}
	if base == nil {
		return idx
	}
	seenTools := map[string]bool{}
	for _, snap := range snapshots {
		for tool := range snap.Schemas {
			seenTools[tool] = true
		}
	}
	ownerSet, prunedOwnerSet := map[string]map[string]bool{}, map[string]map[string]bool{}
	for toolName := range base.Tools {
		if toolcontract.IsCommandOperation(toolName) && !seenTools[toolName] {
			continue
		}
		meta, ok := base.ToolMeta(toolName)
		if !ok {
			continue
		}
		baseProj := tools.ProjectProviderToolMeta(tools.ToolMeta{Name: toolName, ArgsSchema: meta.ArgsSchema}).ArgsSchema
		idx.baseSchemas[toolName] = baseProj
		basePaths := SchemaPropertyPaths(baseProj)
		for _, p := range basePaths {
			addOwner(ownerSet, p, toolName)
		}
		if !seenTools[toolName] {
			continue
		}
		baseEnums := SchemaEnums(baseProj)
		for _, snap := range snapshots {
			projSchema, offered := snap.Schemas[toolName]
			if !offered {
				continue
			}
			projPathSet := stringSliceSet(SchemaPropertyPaths(projSchema))
			for _, p := range basePaths {
				if !projPathSet[p] {
					addOwner(idx.prunedPathsByTool, toolName, p)
					addOwner(prunedOwnerSet, p, toolName)
				}
			}
			projEnums := SchemaEnums(projSchema)
			for p, baseLits := range baseEnums {
				if !projPathSet[p] {
					continue
				}
				projLitSet := stringSliceSet(projEnums[p])
				for _, lit := range baseLits {
					if !projLitSet[lit] {
						idx.prunedEnums[EnumKey{Tool: toolName, Path: p, Literal: lit}] = true
					}
				}
			}
		}
	}
	idx.allPathOwners = flattenOwnerSets(ownerSet)
	idx.prunedPathOwners = flattenOwnerSets(prunedOwnerSet)
	return idx
}

// DiscoverPrunableSchemaIndex projects tool schemas across coordinator turns,
// worker profiles, postures, Ask mode, and wait-condition narrowing states.
func DiscoverPrunableSchemaIndex(ctx context.Context, eff *extpacks.EffectiveCatalog) (*PrunableSchemaIndex, error) {
	if eff == nil {
		var err error
		if eff, err = extpacks.CatalogForConsumers(); err != nil {
			return nil, err
		}
	}
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	if err != nil {
		return nil, err
	}
	reg, err := buildCatalogToolRegistry(eff)
	if err != nil {
		return nil, err
	}
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	if err != nil {
		return nil, err
	}
	postures, err := sessionprofiles.LoadPostureRegistry()
	if err != nil {
		return nil, err
	}
	surfaces, err := surfacecatalog.Load()
	if err != nil {
		return nil, err
	}
	sandboxCfg, err := sandbox.LoadConfig()
	if err != nil {
		return nil, err
	}
	snaps, err := collectProjectedSnapshots(ctx, schemas, reg, sandboxCfg, profiles, postures.List(), surfaces)
	if err != nil {
		return nil, err
	}
	return DiffSchemaProjections(schemas, snaps), nil
}

func collectProjectedSnapshots(ctx context.Context, schemas *toolschema.Config, reg *tools.DefaultRegistry, sandboxCfg sandbox.Config, profiles []sandbox.ToolProfile, postures []sessionprofiles.PostureSpec, surfaces surfacecatalog.Catalog) ([]ProjectedSchemaSnapshot, error) {
	newExec := func(narrowed []sandbox.ToolProfile, extHidden bool) *toolexecution.Executor {
		exec := toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(sandbox.NewBoundary(sandboxCfg, narrowed)), reg, toolprofiles.DefaultToolProfileID)
		exec.Metadata.SetToolSchemas(schemas)
		exec.Metadata.SetCommandExternalHidden(extHidden)
		return exec
	}
	var snaps []ProjectedSchemaSnapshot
	for _, posture := range postures {
		for _, askClamp := range []bool{false, true} {
			narrowed, err := narrowPostureProfiles(profiles, posture.CommandEffects, askClamp)
			if err != nil {
				return nil, err
			}
			for _, extHidden := range []bool{false, true} {
				exec := newExec(narrowed, extHidden)
				for _, prof := range narrowed {
					metas, listErr := exec.Metadata.List(ctx, platform.ToolFilter{ProfileID: prof.ID, ProjectRootCount: 1})
					if listErr != nil {
						return nil, listErr
					}
					if askClamp {
						metas = promptloop.ProjectAskToolMetas(metas)
					}
					snaps = append(snaps, snapshotFromMetas(fmt.Sprintf("prof/%s/%s/%t/%t", prof.ID, posture.ID, askClamp, extHidden), metas))
				}
			}
		}
		narrowed, _ := narrowPostureProfiles(profiles, posture.CommandEffects, false)
		snaps = append(snaps, collectCoordinatorTurnSnapshots(ctx, reg, newExec(narrowed, false), posture.ID, surfaces)...)
	}
	for i, prof := range profiles {
		if len(prof.WaitConditions) == 0 {
			continue
		}
		variants := [][]string{nil}
		for _, cond := range prof.WaitConditions {
			variants = append(variants, []string{cond})
		}
		for vIdx, conds := range variants {
			cloned := slices.Clone(profiles)
			cloned[i].WaitConditions = conds
			metas, err := newExec(cloned, false).Metadata.List(ctx, platform.ToolFilter{ProfileID: prof.ID, ProjectRootCount: 1})
			if err != nil {
				return nil, err
			}
			snaps = append(snaps, snapshotFromMetas(fmt.Sprintf("wait/%s/%d", prof.ID, vIdx), metas))
		}
	}
	return snaps, nil
}

func collectCoordinatorTurnSnapshots(ctx context.Context, reg *tools.DefaultRegistry, exec *toolexecution.Executor, posture api.SessionPosture, surfaces surfacecatalog.Catalog) []ProjectedSchemaSnapshot {
	policy := toolpolicy.NewEngine(toolpolicy.EngineDeps{ToolLister: exec.Metadata, ProjectRootCount: func(context.Context, *api.Session) int { return 1 }})
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Inspect state"}}
	var snaps []ProjectedSchemaSnapshot
	for _, mode := range []api.SessionMode{api.SessionModeAgent, api.SessionModeAsk} {
		for _, surfID := range surfaces.SurfaceIDs() {
			row, err := surfaces.Surface(surfID)
			if err != nil || (mode == api.SessionModeAsk && row.RequiresAgent) {
				continue
			}
			loaded := stringSliceSet(row.LoadableTools())
			deps := promptloop.ContextDeps{
				Tools: reg, Policy: policy,
				ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
					return surface.ImplementSessionState{}
				},
				LoadedTools:        func(string) map[string]bool { return maps.Clone(loaded) },
				ProjectRootCount:   func(context.Context, *api.Session) int { return 1 },
				ContextSurfaceDeps: promptloop.ContextSurfaceDeps{WebSearchEnabled: func() bool { return true }},
			}
			sess := &api.Session{ID: "coord", Mode: mode, Posture: posture, AgentType: prompts.CoordinatorProfileID, WorkspacePath: "/tmp/proj"}
			frame := inject.CoordinatorTurnFrame{
				RunContext: api.CoordinatorRunContext{RunID: "run-1", WorkflowID: "wf-1", PhaseCoordinatorSurface: surfID, AdvanceWhenGateMet: "coordinator", PhaseHostHeld: surfID == "await_host", HasComposeDraft: surfID == "workflow_compose"},
				Machine:    inject.Machine{ProfileID: prompts.CoordinatorProfileID},
			}
			if metas, _, _, projErr := promptloop.ProjectTurnTools(ctx, deps, sess, history, frame); projErr == nil {
				snaps = append(snaps, snapshotFromMetas(fmt.Sprintf("coord/%s/%s/%s", mode, posture, surfID), metas))
			}
		}
	}
	return snaps
}

func normalizeSchemaPath(path string) string {
	return strings.ReplaceAll(strings.TrimSpace(path), "[]", "")
}

func sortedTrueKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, ok := range m {
		if ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func extractStringEnumSlice(raw any) []string {
	var out []string
	switch vals := raw.(type) {
	case []any:
		for _, item := range vals {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, vals...)
	}
	return out
}

func addOwner(m map[string]map[string]bool, k, v string) {
	if m[k] == nil {
		m[k] = map[string]bool{}
	}
	m[k][v] = true
}

func stringSliceSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

func flattenOwnerSets(m map[string]map[string]bool) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, set := range m {
		out[k] = sortedTrueKeys(set)
	}
	return out
}

func snapshotFromMetas(id string, metas []tools.ToolMeta) ProjectedSchemaSnapshot {
	schemas := make(map[string]map[string]any, len(metas))
	for _, m := range metas {
		schemas[m.Name] = m.ArgsSchema
	}
	return ProjectedSchemaSnapshot{StateID: id, Schemas: schemas}
}

func schemaAcceptsPath(schema map[string]any, normPath string) bool {
	return len(schemaNodesAtPath(schema, normPath)) > 0
}

func missingPathHead(schema map[string]any, normPath string) string {
	rem := normPath
	prefix := ""
	for rem != "" {
		seg, tail, _ := strings.Cut(rem, ".")
		if prefix == "" {
			prefix = seg
		} else {
			prefix = prefix + "." + seg
		}
		if !schemaAcceptsPath(schema, prefix) {
			return prefix
		}
		rem = tail
	}
	return normPath
}

func schemaNodesAtPath(schema map[string]any, normPath string) []map[string]any {
	var out []map[string]any
	var walk func(node map[string]any, rem string, depth int)
	walk = func(node map[string]any, rem string, depth int) {
		if node == nil || depth > 12 {
			return
		}
		if rem == "" {
			out = append(out, node)
			return
		}
		if items, ok := node["items"].(map[string]any); ok {
			walk(items, rem, depth+1)
		}
		head, tail, _ := strings.Cut(rem, ".")
		if props, hasProps := node["properties"].(map[string]any); hasProps {
			if child, ok := props[head].(map[string]any); ok {
				walk(child, tail, depth+1)
			} else if apSchema, ok := additionalPropSchema(node); ok {
				walk(apSchema, tail, depth+1)
			}
		} else if apSchema, ok := additionalPropSchema(node); ok {
			walk(apSchema, tail, depth+1)
		}
		for _, comb := range []string{"oneOf", "anyOf", "allOf"} {
			branches, _ := node[comb].([]any)
			for _, item := range branches {
				if br, ok := item.(map[string]any); ok {
					walk(br, rem, depth+1)
				}
			}
		}
	}
	walk(schema, normPath, 0)
	return out
}

func additionalPropSchema(node map[string]any) (map[string]any, bool) {
	switch ap := node["additionalProperties"].(type) {
	case bool:
		if ap {
			return map[string]any{}, true
		}
	case map[string]any:
		return ap, true
	}
	return nil, false
}

func nodesAcceptKind(nodes []map[string]any, want string) bool {
	for _, n := range nodes {
		if nodeAcceptsType(n, want, 0) {
			return true
		}
	}
	return false
}

func nodeAcceptsType(n map[string]any, want string, depth int) bool {
	if n == nil || depth > 5 {
		return false
	}
	if len(n) == 0 {
		return true
	}
	switch t := n["type"].(type) {
	case string:
		if t == want || (t == "number" && want == "integer") {
			return true
		}
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && (s == want || (s == "number" && want == "integer")) {
				return true
			}
		}
	}
	for _, comb := range []string{"oneOf", "anyOf", "allOf"} {
		branches, _ := n[comb].([]any)
		for _, item := range branches {
			if br, ok := item.(map[string]any); ok && nodeAcceptsType(br, want, depth+1) {
				return true
			}
		}
	}
	return false
}

func nodesAcceptBool(nodes []map[string]any, val bool) bool {
	if !nodesAcceptKind(nodes, "boolean") {
		return false
	}
	for _, n := range nodes {
		if rawEnum, has := n["enum"].([]any); has && len(rawEnum) > 0 {
			for _, item := range rawEnum {
				if b, ok := item.(bool); ok && b == val {
					return true
				}
			}
			continue
		}
		if nodeAcceptsType(n, "boolean", 0) {
			return true
		}
	}
	return true
}

func collectEnumsFromNodes(nodes []map[string]any) []string {
	seen := map[string]bool{}
	var walk func(map[string]any, int)
	walk = func(n map[string]any, depth int) {
		if n == nil || depth > 5 {
			return
		}
		for _, lit := range extractStringEnumSlice(n["enum"]) {
			seen[lit] = true
		}
		for _, comb := range []string{"oneOf", "anyOf", "allOf"} {
			branches, _ := n[comb].([]any)
			for _, item := range branches {
				if br, ok := item.(map[string]any); ok {
					walk(br, depth+1)
				}
			}
		}
	}
	for _, n := range nodes {
		walk(n, 0)
	}
	return sortedTrueKeys(seen)
}

func requiredSet(schema map[string]any) map[string]bool {
	req, _ := toolschema.ArgFieldSummary(schema)
	return stringSliceSet(req)
}

func materializeCallArgs(schema map[string]any, args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = materializeValueAt(schema, k, v)
	}
	return out
}

func materializeValueAt(rootSchema map[string]any, path string, val any) any {
	nodes := schemaNodesAtPath(rootSchema, path)
	switch typed := val.(type) {
	case placeholderValue:
		return dummyValueForNodes(nodes, typed.numeric)
	case unquotedIdent:
		return string(typed)
	case string:
		if isPlaceholderText(typed) {
			return dummyStringForNodes(nodes)
		}
		return typed
	case map[string]any:
		m := make(map[string]any, len(typed))
		for k, child := range typed {
			m[k] = materializeValueAt(rootSchema, path+"."+k, child)
		}
		return m
	case []any:
		arr := make([]any, len(typed))
		for i, elem := range typed {
			if em, ok := elem.(map[string]any); ok {
				arr[i] = materializeValueAt(rootSchema, path, em)
			} else if _, isPH := elem.(placeholderValue); isPH {
				arr[i] = dummyItemForArrayNodes(nodes)
			} else if s, ok := elem.(string); ok && isPlaceholderText(s) {
				arr[i] = "item-1"
			} else if id, ok := elem.(unquotedIdent); ok {
				arr[i] = string(id)
			} else {
				arr[i] = elem
			}
		}
		return arr
	default:
		return val
	}
}

func dummyValueForNodes(nodes []map[string]any, preferNum bool) any {
	if preferNum || nodesAcceptKind(nodes, "integer") {
		return int64(1)
	}
	if nodesAcceptKind(nodes, "number") {
		return float64(1)
	}
	if nodesAcceptKind(nodes, "boolean") {
		return true
	}
	if nodesAcceptKind(nodes, "array") {
		return []any{dummyItemForArrayNodes(nodes)}
	}
	if nodesAcceptKind(nodes, "object") {
		return dummyObjectForNodes(nodes)
	}
	return dummyStringForNodes(nodes)
}

func dummyStringForNodes(nodes []map[string]any) string {
	if enums := collectEnumsFromNodes(nodes); len(enums) > 0 {
		return enums[0]
	}
	for _, n := range nodes {
		if f, _ := n["format"].(string); f == "uuid" {
			return "00000000-0000-4000-8000-000000000001"
		}
	}
	return "sample"
}

func dummyItemForArrayNodes(nodes []map[string]any) any {
	var itemNodes []map[string]any
	for _, n := range nodes {
		if items, ok := n["items"].(map[string]any); ok {
			itemNodes = append(itemNodes, items)
		}
	}
	if len(itemNodes) > 0 {
		return dummyValueForNodes(itemNodes, false)
	}
	return "item-1"
}

func dummyObjectForNodes(nodes []map[string]any) map[string]any {
	out := map[string]any{}
	for _, n := range nodes {
		props, _ := n["properties"].(map[string]any)
		for _, req := range extractStringEnumSlice(n["required"]) {
			if child, ok := props[req].(map[string]any); ok {
				out[req] = dummyValueForNodes([]map[string]any{child}, false)
			}
		}
		for _, comb := range []string{"anyOf", "oneOf"} {
			if branches, _ := n[comb].([]any); len(branches) > 0 {
				if br, ok := branches[0].(map[string]any); ok {
					for _, req := range extractStringEnumSlice(br["required"]) {
						if child, ok := props[req].(map[string]any); ok {
							out[req] = dummyValueForNodes([]map[string]any{child}, false)
						}
					}
				}
			}
		}
	}
	return out
}
