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
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// ReaderInput is what a render knows about who will read it.
type ReaderInput struct {
	Host           promptunit.Host
	ProfileID      string
	SurfaceID      string
	Mode           string
	Profiles       []sandbox.ToolProfile
	Plan           toolsurface.Plan
	Loaded         map[string]bool
	Visible        []string
	Schemas        *toolschema.Config
	Catalog        *extpacks.EffectiveCatalog
	RootCount      int
	HasGit         bool
	OnWorkerBranch bool
	WebSearch      bool
}

// Reader is an immutable projection of a prompt reader's tool reach and facts.
type Reader struct {
	valid          bool
	host           promptunit.Host
	profileID      string
	surfaceID      string
	mode           string
	profiles       []sandbox.ToolProfile
	plan           toolsurface.Plan
	loaded         map[string]bool
	visible        []string
	schemas        *toolschema.Config
	catalog        *extpacks.EffectiveCatalog
	rootCount      int
	hasGit         bool
	onWorkerBranch bool
	webSearch      bool
	offered        []string
	requestable    []string
	commandOnFloor bool
	effects        toolcontract.CommandEffects
	held           map[string]bool
	mints          map[string]bool
}

// NewReader constructs a validated Reader from the executor's inputs.
func NewReader(in ReaderInput) (Reader, error) {
	if len(in.Profiles) == 0 {
		return Reader{}, fmt.Errorf("reader requires tool profiles")
	}
	profileID := strings.TrimSpace(in.ProfileID)
	var profile sandbox.ToolProfile
	for _, p := range in.Profiles {
		if p.ID == profileID {
			profile = p
			break
		}
	}
	if profile.ID == "" {
		return Reader{}, fmt.Errorf("tool profile %q not found", profileID)
	}
	surfaceID := strings.TrimSpace(in.SurfaceID)
	if surfaceID != "" && !in.Plan.Compiled() {
		return Reader{}, fmt.Errorf("coordinator surface %q requires a compiled tool plan", surfaceID)
	}

	host := in.Host
	if host == "" {
		if profileID == CoordinatorProfileID || surfaceID != "" {
			host = promptunit.HostCoordinator
		} else {
			host = promptunit.HostWorker
		}
	}

	profileEffects := profile.CommandEffects

	var (
		effects        toolcontract.CommandEffects
		offered        []string
		requestable    []string
		commandOnFloor bool
	)

	switch {
	case surfaceID != "":
		cat, err := surfacecatalog.Load()
		if err != nil {
			return Reader{}, err
		}
		row, err := cat.Surface(surfaceID)
		if err != nil {
			return Reader{}, err
		}
		surfaceEffects, err := cat.CommandEffects(surfaceID)
		if err != nil {
			return Reader{}, err
		}
		effects = surfaceEffects & profileEffects

		floor := availableToolsInPlan(row.FloorTools(), in.Plan)
		loadable := availableToolsInPlan(row.LoadableTools(), in.Plan)
		sort.Strings(floor)
		sort.Strings(loadable)
		if in.Visible != nil {
			offered = availableToolsInPlan(slices.Clone(in.Visible), in.Plan)
			sort.Strings(offered)
			offeredSet := toolNameSet(offered)
			for _, name := range loadable {
				if !offeredSet[name] {
					requestable = append(requestable, name)
				}
			}
		} else {
			offered, requestable = OfferedTools(floor, loadable, in.Loaded)
		}
		if !in.WebSearch {
			offered = withoutResearchTools(offered)
			requestable = withoutResearchTools(requestable)
		}
		commandOnFloor = slices.Contains(floor, "command")

	case profileID == CoordinatorProfileID:
		effects = profileEffects
		if in.Plan.Compiled() {
			floor := in.Plan.ImmediateNames()
			loadable := in.Plan.DeferredNames()
			offered, requestable = OfferedTools(floor, loadable, in.Loaded)
			commandOnFloor = slices.Contains(floor, "command")
		} else {
			offered = visibleToolNames(profile, in.Visible)
			commandOnFloor = slices.Contains(offered, "command")
		}
		if !in.WebSearch {
			offered = withoutResearchTools(offered)
			requestable = withoutResearchTools(requestable)
		}

	default:
		effects = profileEffects
		toolNames := availableToolsInPlan(visibleToolNames(profile, in.Visible), in.Plan)
		for _, name := range toolNames {
			if (profile.ToolDeferred(name) && !in.Loaded[name]) || !profile.ToolAllowed(name) {
				requestable = append(requestable, name)
				continue
			}
			offered = append(offered, name)
		}
		sort.Strings(offered)
		sort.Strings(requestable)
		commandOnFloor = slices.Contains(offered, "command") && !profile.ToolDeferred("command")
	}

	held := make(map[string]bool, len(offered)+len(requestable)+3)
	for _, name := range offered {
		held[name] = true
	}
	for _, name := range requestable {
		held[name] = true
	}
	for mark, ok := range toolcontract.ReachMarks(held["command"], effects) {
		if ok {
			held[mark] = true
		}
	}
	mints := evidenceMints(held)

	return Reader{
		valid:          true,
		host:           host,
		profileID:      profileID,
		surfaceID:      surfaceID,
		mode:           strings.TrimSpace(in.Mode),
		profiles:       slices.Clone(in.Profiles),
		plan:           in.Plan,
		loaded:         maps.Clone(in.Loaded),
		visible:        slices.Clone(in.Visible),
		schemas:        in.Schemas,
		catalog:        in.Catalog,
		rootCount:      in.RootCount,
		hasGit:         in.HasGit,
		onWorkerBranch: in.OnWorkerBranch,
		webSearch:      in.WebSearch,
		offered:        offered,
		requestable:    requestable,
		commandOnFloor: commandOnFloor,
		effects:        effects,
		held:           held,
		mints:          mints,
	}, nil
}

// Valid reports whether r was constructed by NewReader.
func (r Reader) Valid() bool { return r.valid }

// Input returns a copy of the inputs r was constructed from.
func (r Reader) Input() ReaderInput {
	return ReaderInput{
		Host:           r.host,
		ProfileID:      r.profileID,
		SurfaceID:      r.surfaceID,
		Mode:           r.mode,
		Profiles:       slices.Clone(r.profiles),
		Plan:           r.plan,
		Loaded:         maps.Clone(r.loaded),
		Visible:        slices.Clone(r.visible),
		Schemas:        r.schemas,
		Catalog:        r.catalog,
		RootCount:      r.rootCount,
		HasGit:         r.hasGit,
		OnWorkerBranch: r.onWorkerBranch,
		WebSearch:      r.webSearch,
	}
}

// Host returns the prompt unit host (coordinator or worker).
func (r Reader) Host() promptunit.Host { return r.host }

// ProfileID returns the reader's tool profile ID.
func (r Reader) ProfileID() string { return r.profileID }

// SurfaceID returns the coordinator surface ID, or "" for worker readers.
func (r Reader) SurfaceID() string { return r.surfaceID }

// Profiles returns a copy of the reader's posture-narrowed tool profiles.
func (r Reader) Profiles() []sandbox.ToolProfile { return slices.Clone(r.profiles) }

// Catalog returns the reader's effective pack catalog.
func (r Reader) Catalog() *extpacks.EffectiveCatalog { return r.catalog }

// RootCount returns the project root count.
func (r Reader) RootCount() int { return r.rootCount }

// WebSearch reports whether web search and fetch are enabled.
func (r Reader) WebSearch() bool { return r.webSearch }

// Offered returns the sorted tools offered on this request.
func (r Reader) Offered() []string { return slices.Clone(r.offered) }

// Requestable returns the sorted tools the reader can load on this turn.
func (r Reader) Requestable() []string { return slices.Clone(r.requestable) }

// Effects returns the command effects admitted for this reader.
func (r Reader) Effects() toolcontract.CommandEffects { return r.effects }

// Held returns the reach set: offered and requestable tools plus command reach marks.
func (r Reader) Held() map[string]bool { return maps.Clone(r.held) }

// Mints returns the evidence-kind reach map for this reader.
func (r Reader) Mints() map[string]bool { return maps.Clone(r.mints) }

// Selection returns the promptunit.Selection for this reader.
func (r Reader) Selection() promptunit.Selection {
	return UnitSelectionVars(r.host, r.mode, r.offered)
}

// Facts builds the template variable map describing this reader.
func (r Reader) Facts() map[string]any {
	out := make(map[string]any, 64)
	if !r.valid {
		return out
	}

	if r.surfaceID != "" || r.profileID == CoordinatorProfileID {
		var scopeData CoordinatorPathScopeData
		var err error
		if r.surfaceID == "implement_investigate" {
			scopeData, err = LoadInvestigatePathScopes()
		} else {
			scopeData, err = CoordinatorPathScopesFromProfiles(r.profiles)
		}
		if err == nil {
			for k, v := range CoordinatorPathScopeTemplateVars(scopeData) {
				out[k] = v
			}
		}
		for k, v := range CoordinatorPolicyTemplateVars(r.offered, r.requestable) {
			out[k] = v
		}
		spawn.RefreshSizingHintVars(out)
		out["is_coordinator"] = true
		out["on_worker_branch"] = r.onWorkerBranch
		out["has_git"] = r.hasGit
		out["root_count"] = r.rootCount
		out["command_on_floor"] = r.commandOnFloor
		if r.surfaceID != "" {
			out["surface_id"] = r.surfaceID
		}
		if r.mode != "" {
			out["execution_mode"] = r.mode
		}
		restrictCommandEffects(r.effects, r.offered, r.requestable, out)
		syncCommandExternalFacts(out)
		roster, err := RequestableTools(r.requestable, r.schemas)
		if err == nil {
			out["requestable_tools"] = requestableToolVars(roster)
			out["requestable_capabilities"] = RequestableCapabilities(roster)
		}
		out["surface_offered"] = slices.Clone(r.offered)
		commandMutates := currentCommandEffectFlags(out, slices.Contains(r.offered, "command")).mutation
		if r.surfaceID != "" {
			if card, err := LoadCoordinatorSurfaceCard(r.surfaceID); err == nil {
				for k, v := range CoordinatorSurfaceCardVars(card, r.offered, r.requestable, commandMutates).TemplateVars() {
					out[k] = v
				}
			}
		}
		runOffered := r.offered
		if !commandMutates {
			runOffered = filterToolName(r.offered, "command")
		}
		MergeVisualShowVars(runOffered, r.requestable, out)
		out["more_tools_loadable"] = len(r.requestable) > 0
		out["web_search_enabled"] = r.webSearch
		if !r.webSearch {
			MergeWebResearchUnavailable(out)
		}
		clearSkillRosterWithoutSkillsRead(out)
		return out
	}

	var profile sandbox.ToolProfile
	for _, p := range r.profiles {
		if p.ID == r.profileID {
			profile = p
			break
		}
	}
	toolViews := make([]AgentToolArgView, 0, len(r.offered))
	for _, name := range r.offered {
		view := AgentToolArgView{Name: name}
		if r.schemas != nil {
			if meta, ok := r.schemas.ToolMeta(name); ok {
				view.RequiredArgs, _ = toolschema.ArgFieldSummary(meta.ArgsSchema)
			}
		}
		toolViews = append(toolViews, view)
	}
	roster, _ := RequestableTools(r.requestable, r.schemas)
	data := AgentToolSurfaceData{
		ToolProfile:     r.profileID,
		Tools:           toolViews,
		Requestable:     roster,
		WriteGlobs:      slices.Clone(profile.WriteGlobs),
		CommandEffects:  r.effects,
		CommandDeferred: !r.commandOnFloor && slices.Contains(r.offered, "command"),
	}
	for k, v := range AgentToolSurfaceTemplateVars(data) {
		out[k] = v
	}
	restrictCommandEffects(r.effects, r.offered, r.requestable, out)
	syncCommandExternalFacts(out)
	mergeVisualShowVarsFromSurface(data, out)
	out["is_coordinator"] = false
	out["on_worker_branch"] = r.onWorkerBranch
	out["has_git"] = r.hasGit
	out["root_count"] = r.rootCount
	out["web_search_enabled"] = r.webSearch
	return out
}

// Fingerprint returns a stable digest of the reader's inputs and derived reach.
func (r Reader) Fingerprint() string {
	payload, err := json.Marshal(struct {
		Host           promptunit.Host
		ProfileID      string
		SurfaceID      string
		Mode           string
		Offered        []string
		Requestable    []string
		CommandOnFloor bool
		Effects        uint8
		RootCount      int
		HasGit         bool
		OnWorkerBranch bool
		WebSearch      bool
	}{
		Host:           r.host,
		ProfileID:      r.profileID,
		SurfaceID:      r.surfaceID,
		Mode:           r.mode,
		Offered:        r.offered,
		Requestable:    r.requestable,
		CommandOnFloor: r.commandOnFloor,
		Effects:        uint8(r.effects),
		RootCount:      r.rootCount,
		HasGit:         r.hasGit,
		OnWorkerBranch: r.onWorkerBranch,
		WebSearch:      r.webSearch,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func availableToolsInPlan(names []string, plan toolsurface.Plan) []string {
	if !plan.Compiled() {
		return names
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if plan.Addressable(name) {
			out = append(out, name)
		}
	}
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
