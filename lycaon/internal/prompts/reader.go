package prompts

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// HostFacts are the session facts every Reader for one session shares.
type HostFacts struct {
	SessionMode string
	// Profiles are the session's catalog profiles constrained by its posture.
	Profiles []sandbox.ToolProfile
	Schemas  *toolschema.Config
	// HasGit covers the primary root or the worker branch; HasAnyGitRoot any root.
	RootCount             int
	HasGit                bool
	HasAnyGitRoot         bool
	OnWorkerBranch        bool
	WebSearch             bool
	ReviewsConfinedWrites bool
	CommandExternalHidden bool
	SessionScratchDir     string
	SharedTempLocations   []string
	// SpawnAllowlist is the declared dispatch roster; the reader applies the
	// turn's lane, mode, root and web-search exclusions.
	SpawnAllowlist []string
}

// TurnProjection is the tool projection one model request carries.
type TurnProjection struct {
	ProfileID string
	SurfaceID string
	Plan      toolsurface.Plan
	// Offered names the tools on the request; only plan-immediate names count.
	Offered []string
	// ProjectedSchemas are the request's argument schemas, offered and deferred.
	ProjectedSchemas map[string]map[string]any
	// Declared projects the profile's or surface's declared tools for a render
	// without a model request: NewReader compiles the plan from the host's
	// profiles and root count, promotes Offered, and nil Offered offers the
	// plan's immediate tools.
	Declared bool
}

// Reader is an immutable projection of a prompt reader's tool reach and facts.
type Reader struct {
	host        HostFacts
	turn        TurnProjection
	profileID   string
	surfaceID   string
	offered     []string
	requestable []string
	effects     toolcontract.CommandEffects
	held        map[string]bool
	mints       map[string]bool
	selection   promptunit.Selection
	facts       map[string]any
	schemas     map[string]map[string]any
	fingerprint string
}

// NewReader derives a Reader from a session's host facts and one request's
// tool projection.
func NewReader(host HostFacts, turn TurnProjection) (Reader, error) {
	if len(host.Profiles) == 0 {
		return Reader{}, fmt.Errorf("reader requires tool profiles")
	}
	profileID := strings.TrimSpace(turn.ProfileID)
	var profile sandbox.ToolProfile
	for _, p := range host.Profiles {
		if p.ID == profileID {
			profile = p
			break
		}
	}
	if profile.ID == "" {
		return Reader{}, fmt.Errorf("tool profile %q not found", profileID)
	}
	surfaceID := strings.TrimSpace(turn.SurfaceID)
	if surfaceID != "" {
		cat, err := surfacecatalog.Load()
		if err != nil {
			return Reader{}, err
		}
		if _, err := cat.Surface(surfaceID); err != nil {
			return Reader{}, err
		}
	}
	plan, offeredNames, err := resolveTurnPlan(profile, surfaceID, host.RootCount, turn)
	if err != nil {
		return Reader{}, err
	}

	host.SessionMode = normalizeSessionMode(host.SessionMode)
	resolved := turn
	resolved.ProfileID, resolved.SurfaceID, resolved.Plan, resolved.Offered = profileID, surfaceID, plan, offeredNames

	hostKind := promptunit.HostWorker
	if profileID == CoordinatorProfileID || surfaceID != "" {
		hostKind = promptunit.HostCoordinator
	}
	effects, err := surfacecatalog.AdmittedCommandEffects(host.Profiles, profileID, surfaceID)
	if err != nil {
		return Reader{}, err
	}
	offered, requestable := projectReach(plan, offeredNames, host)
	commandOnFloor := plan.Immediate("command")
	if hostKind == promptunit.HostWorker {
		commandOnFloor = slices.Contains(offered, "command") && !profile.ToolDeferred("command")
	}

	// A requestable tool is not held until it loads: copy that names it would
	// name a tool the request does not offer.
	held := readerProfileHeldSet(profile, toolNameSet(offered), effects, host.CommandExternalHidden)
	mode := defaultExecutionModeForSurface(surfaceID)
	facts, effectiveSchemas, err := buildReaderFacts(host, resolved, profile, mode, offered, requestable, commandOnFloor, effects)
	if err != nil {
		return Reader{}, err
	}

	retained := resolved
	retained.ProjectedSchemas = nil
	return Reader{
		host:        host,
		turn:        retained,
		profileID:   profileID,
		surfaceID:   surfaceID,
		offered:     offered,
		requestable: requestable,
		effects:     effects,
		held:        held,
		mints:       evidenceMints(held),
		selection: promptunit.Selection{
			Host: hostKind,
			Mode: mode,
			Held: held,
		},
		facts:       facts,
		schemas:     effectiveSchemas,
		fingerprint: computeReaderFingerprint(hostKind, profile, mode, offered, requestable, commandOnFloor, effects, effectiveSchemas, host, resolved),
	}, nil
}

// Restricted derives the Reader of a request that offers only names from r's
// immediate tools, with r's host facts and nothing requestable.
func (r Reader) Restricted(names []string) (Reader, error) {
	if !r.Valid() {
		return Reader{}, fmt.Errorf("restrict an unconstructed reader")
	}
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if r.turn.Plan.Immediate(name) {
			kept = append(kept, name)
		}
	}
	turn := r.turn
	turn.Plan = toolsurface.Compile(kept, nil)
	turn.Offered = kept
	turn.Declared = false
	turn.ProjectedSchemas = make(map[string]map[string]any, len(kept))
	for _, name := range kept {
		if schema, ok := r.schemas[name]; ok {
			turn.ProjectedSchemas[name] = schema
		}
	}
	return NewReader(r.host, turn)
}

func resolveTurnPlan(profile sandbox.ToolProfile, surfaceID string, rootCount int, turn TurnProjection) (toolsurface.Plan, []string, error) {
	if !turn.Declared {
		if !turn.Plan.Compiled() {
			return toolsurface.Plan{}, nil, fmt.Errorf("reader for %q requires the request's compiled tool plan", profile.ID)
		}
		return turn.Plan, turn.Offered, nil
	}
	var plan toolsurface.Plan
	if surfaceID != "" {
		compiled, err := CompileSurfacePlan(surfaceID, rootCount)
		if err != nil {
			return toolsurface.Plan{}, nil, err
		}
		plan = compiled
	} else {
		var immediate, deferred []string
		for _, name := range visibleToolNames(profile, nil) {
			switch {
			case !profile.ToolAllowed(name):
			case profile.ToolDeferred(name):
				deferred = append(deferred, name)
			default:
				immediate = append(immediate, name)
			}
		}
		plan = toolsurface.Compile(immediate, deferred)
	}
	if turn.Offered == nil {
		return plan, plan.ImmediateNames(), nil
	}
	return plan.Promote(turn.Offered...), turn.Offered, nil
}

// projectReach offers the request's plan-immediate names and makes the plan's
// remaining deferred names requestable, without research tools when web search
// is off and without catalog tools Ask mode refuses.
func projectReach(plan toolsurface.Plan, names []string, host HostFacts) (offered, requestable []string) {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || !plan.Immediate(name) {
			continue
		}
		seen[name] = true
		offered = append(offered, name)
	}
	for _, name := range plan.DeferredNames() {
		if seen[name] {
			continue
		}
		if host.SessionMode == "ask" {
			if contract, ok := toolcontract.Lookup(name); ok && !contract.AskAllowed {
				continue
			}
		}
		requestable = append(requestable, name)
	}
	if !host.WebSearch {
		offered = withoutResearchTools(offered)
		requestable = withoutResearchTools(requestable)
	}
	sort.Strings(offered)
	sort.Strings(requestable)
	return offered, requestable
}

func defaultExecutionModeForSurface(surfaceID string) string {
	switch strings.TrimSpace(surfaceID) {
	case "implement_investigate":
		return "investigate"
	case "implement_synthesis":
		return "wrapup"
	case "implement_routing", "implement_dispatch", "implement_overlay_promote", "implement_park":
		return "orchestrate"
	default:
		return ""
	}
}

func normalizeSessionMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "ask") {
		return "ask"
	}
	return "agent"
}

func readerProfileHeldSet(
	profile sandbox.ToolProfile,
	tools map[string]bool,
	effects toolcontract.CommandEffects,
	externalHidden bool,
) map[string]bool {
	held := make(map[string]bool, len(tools)+4)
	for name, ok := range tools {
		if ok {
			held[name] = true
		}
	}
	for mark, ok := range toolcontract.ReachMarks(held["command"], effects) {
		if ok {
			held[mark] = true
		}
	}
	if externalHidden {
		delete(held, toolcontract.ReachCommandExternalExecSelector)
	}
	if len(profile.DenyTools) > 0 {
		for mark, ok := range toolcontract.DeniedCommandMarks(held, effects, profile.ToolDenied) {
			if ok {
				held[mark] = true
			}
		}
	}
	return held
}

// Valid reports whether r was constructed by NewReader.
func (r Reader) Valid() bool { return r.profileID != "" }

// WithVision returns a copy of r with the model vision capability fact set.
func (r Reader) WithVision(vision bool) Reader {
	r.facts = maps.Clone(r.facts)
	r.facts["caps"] = map[string]any{"vision": vision}
	if vision {
		r.fingerprint += ":v1"
	} else {
		r.fingerprint += ":v0"
	}
	return r
}

// ProfileID returns the reader's tool profile ID.
func (r Reader) ProfileID() string { return r.profileID }

// SurfaceID returns the coordinator surface ID, or "" for worker readers.
func (r Reader) SurfaceID() string { return r.surfaceID }

// RootCount returns the project root count.
func (r Reader) RootCount() int {
	rc, _ := r.facts["root_count"].(int)
	return rc
}

// WebSearch reports whether web search and fetch are enabled.
func (r Reader) WebSearch() bool {
	ws, _ := r.facts["web_search_enabled"].(bool)
	return ws
}

// Offered returns the sorted tools offered on this request.
func (r Reader) Offered() []string { return slices.Clone(r.offered) }

// Requestable returns the sorted tools the reader can load on this turn.
func (r Reader) Requestable() []string { return slices.Clone(r.requestable) }

// Effects returns the command effects admitted for this reader.
func (r Reader) Effects() toolcontract.CommandEffects { return r.effects }

// Held returns the reach set: offered tools plus command reach marks.
func (r Reader) Held() map[string]bool { return maps.Clone(r.held) }

// CanReachProgram reports whether r can execute name as a command program.
func (r Reader) CanReachProgram(name string) bool {
	return toolcontract.CommandLanguageReachable(name, r.held)
}

// Mints returns the evidence-kind reach map for this reader.
func (r Reader) Mints() map[string]bool { return maps.Clone(r.mints) }

// Selection returns the promptunit.Selection for this reader.
func (r Reader) Selection() promptunit.Selection {
	return promptunit.Selection{
		Host: r.selection.Host,
		Mode: r.selection.Mode,
		Held: maps.Clone(r.selection.Held),
	}
}

// Facts returns a copy of the template variable map describing this reader.
func (r Reader) Facts() map[string]any { return maps.Clone(r.facts) }

// Fingerprint returns a stable digest of the reader's inputs and derived reach.
func (r Reader) Fingerprint() string { return r.fingerprint }

// ProjectedSchema returns the argument schema the request carries for an
// offered tool.
func (r Reader) ProjectedSchema(name string) map[string]any { return r.schemas[name] }

func computeReaderFingerprint(
	hostKind promptunit.Host,
	profile sandbox.ToolProfile,
	mode string,
	offered, requestable []string,
	commandOnFloor bool,
	effects toolcontract.CommandEffects,
	effectiveSchemas map[string]map[string]any,
	host HostFacts,
	turn TurnProjection,
) string {
	payload, err := json.Marshal(struct {
		Host                   promptunit.Host
		ProfileID              string
		SurfaceID              string
		Mode                   string
		SessionMode            string
		Offered                []string
		Requestable            []string
		DenyTools              []string
		WaitConditions         []string
		ExplicitProjected      bool
		ProjectedSchemasDigest map[string]string
		CommandOnFloor         bool
		Effects                uint8
		RootCount              int
		HasGit                 bool
		HasAnyGitRoot          bool
		OnWorkerBranch         bool
		WebSearch              bool
		SpawnAllowlist         []string
		ReviewsConfinedWrites  bool
		CommandExternalHidden  bool
		SessionScratchDir      string
		SharedTempLocations    []string
	}{
		Host:                   hostKind,
		ProfileID:              turn.ProfileID,
		SurfaceID:              turn.SurfaceID,
		Mode:                   mode,
		SessionMode:            host.SessionMode,
		Offered:                offered,
		Requestable:            requestable,
		DenyTools:              normalizedSortedStrings(profile.DenyTools),
		WaitConditions:         normalizedSortedStrings(profile.WaitConditions),
		ExplicitProjected:      turn.ProjectedSchemas != nil,
		ProjectedSchemasDigest: projectedSchemasDigest(offered, effectiveSchemas),
		CommandOnFloor:         commandOnFloor,
		Effects:                uint8(effects),
		RootCount:              host.RootCount,
		HasGit:                 host.HasGit,
		HasAnyGitRoot:          host.HasGit || host.HasAnyGitRoot,
		OnWorkerBranch:         host.OnWorkerBranch,
		WebSearch:              host.WebSearch,
		SpawnAllowlist:         normalizedSortedStrings(host.SpawnAllowlist),
		ReviewsConfinedWrites:  host.ReviewsConfinedWrites,
		CommandExternalHidden:  host.CommandExternalHidden,
		SessionScratchDir:      strings.TrimSpace(host.SessionScratchDir),
		SharedTempLocations:    slices.Clone(host.SharedTempLocations),
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func projectedSchemasDigest(offered []string, effective map[string]map[string]any) map[string]string {
	if len(effective) == 0 {
		return nil
	}
	out := make(map[string]string, len(offered))
	for _, name := range offered {
		if schema, ok := effective[name]; ok && schema != nil {
			raw, err := json.Marshal(schema)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(raw)
			out[name] = base64.RawURLEncoding.EncodeToString(sum[:])
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizedSortedStrings(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// CompileSurfacePlan resolves a coordinator surface's floor as immediate and
// loadable tools as deferred at rootCount.
func CompileSurfacePlan(surfaceID string, rootCount int) (toolsurface.Plan, error) {
	cat, err := surfacecatalog.Load()
	if err != nil {
		return toolsurface.Plan{}, err
	}
	row, err := cat.Surface(surfaceID)
	if err != nil {
		return toolsurface.Plan{}, err
	}
	floor, err := filterToolsForRootCount(row.FloorTools(), rootCount)
	if err != nil {
		return toolsurface.Plan{}, err
	}
	loadable, err := filterToolsForRootCount(row.LoadableTools(), rootCount)
	if err != nil {
		return toolsurface.Plan{}, err
	}
	return toolsurface.Compile(floor, loadable), nil
}

func filterToolsForRootCount(tools []string, rootCount int) ([]string, error) {
	if rootCount != 0 {
		return slices.Clone(tools), nil
	}
	if _, err := toolscope.NoFolderAllowlist(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tools))
	for _, name := range tools {
		if toolscope.AllowedWithNoFolder(name) {
			out = append(out, name)
		}
	}
	return out, nil
}
