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

// ReaderInput is what a render knows about who will read it.
type ReaderInput struct {
	ProfileID             string
	SurfaceID             string
	Mode                  string
	SessionMode           string
	Profiles              []sandbox.ToolProfile
	Plan                  toolsurface.Plan
	Loaded                map[string]bool
	Visible               []string
	Schemas               *toolschema.Config
	ProjectedSchemas      map[string]map[string]any
	RootCount             int
	HasGit                bool
	HasAnyGitRoot         bool
	OnWorkerBranch        bool
	WebSearch             bool
	SpawnAllowlist        []string
	ReviewsConfinedWrites bool
	CommandExternalHidden bool
	SessionScratchDir     string
	SharedTempLocations   []string
}

// Reader is an immutable projection of a prompt reader's tool reach and facts.
type Reader struct {
	profileID   string
	surfaceID   string
	offered     []string
	requestable []string
	effects     toolcontract.CommandEffects
	held        map[string]bool
	mints       map[string]bool
	selection   promptunit.Selection
	facts       map[string]any
	fingerprint string
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

	host := promptunit.HostWorker
	if profileID == CoordinatorProfileID || surfaceID != "" {
		host = promptunit.HostCoordinator
	}

	effects, err := surfacecatalog.AdmittedCommandEffects(in.Profiles, profileID, surfaceID)
	if err != nil {
		return Reader{}, err
	}

	var (
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
		if _, err := cat.Surface(surfaceID); err != nil {
			return Reader{}, err
		}
		floor := in.Plan.ImmediateNames()
		loadable := in.Plan.DeferredNames()
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
		toolNames := availableToolsInPlan(visibleToolNames(profile, in.Visible), in.Plan)
		for _, name := range toolNames {
			deferred := profile.ToolDeferred(name) || (in.Plan.Compiled() && in.Plan.Deferred(name))
			if (deferred && !in.Loaded[name]) || !profile.ToolAllowed(name) {
				requestable = append(requestable, name)
				continue
			}
			offered = append(offered, name)
		}
		sort.Strings(offered)
		sort.Strings(requestable)
		commandOnFloor = slices.Contains(offered, "command") && !profile.ToolDeferred("command")
	}

	// A requestable tool is not held until it loads: copy that names it would
	// name a tool the request does not offer.
	held := readerProfileHeldSet(profile, toolNameSet(offered), effects, in.CommandExternalHidden)
	mints := evidenceMints(held)
	mode := strings.TrimSpace(in.Mode)
	if mode == "" && surfaceID != "" {
		mode = defaultExecutionModeForSurface(surfaceID)
	}
	facts, err := buildReaderFacts(in, profile, profileID, surfaceID, mode, offered, requestable, commandOnFloor, effects)
	if err != nil {
		return Reader{}, err
	}

	return Reader{
		profileID:   profileID,
		surfaceID:   surfaceID,
		offered:     offered,
		requestable: requestable,
		effects:     effects,
		held:        held,
		mints:       mints,
		selection: promptunit.Selection{
			Host: host,
			Mode: mode,
			Held: held,
		},
		facts:       facts,
		fingerprint: computeReaderFingerprint(host, profile, profileID, surfaceID, mode, offered, requestable, commandOnFloor, effects, in),
	}, nil
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

func readerHeldSet(tools map[string]bool, effects toolcontract.CommandEffects, externalHidden bool) map[string]bool {
	return readerProfileHeldSet(sandbox.ToolProfile{}, tools, effects, externalHidden)
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

func computeReaderFingerprint(
	host promptunit.Host,
	profile sandbox.ToolProfile,
	profileID, surfaceID, mode string,
	offered, requestable []string,
	commandOnFloor bool,
	effects toolcontract.CommandEffects,
	in ReaderInput,
) string {
	payload, err := json.Marshal(struct {
		Host                  promptunit.Host
		ProfileID             string
		SurfaceID             string
		Mode                  string
		SessionMode           string
		Offered               []string
		Requestable           []string
		DenyTools             []string
		WaitConditions        []string
		ProjectedFieldPaths   map[string][]string
		CommandOnFloor        bool
		Effects               uint8
		RootCount             int
		HasGit                bool
		HasAnyGitRoot         bool
		OnWorkerBranch        bool
		WebSearch             bool
		SpawnAllowlist        []string
		ReviewsConfinedWrites bool
		CommandExternalHidden bool
		SessionScratchDir     string
		SharedTempLocations   []string
	}{
		Host:                  host,
		ProfileID:             profileID,
		SurfaceID:             surfaceID,
		Mode:                  mode,
		SessionMode:           normalizeSessionMode(in.SessionMode),
		Offered:               offered,
		Requestable:           requestable,
		DenyTools:             normalizedSortedStrings(profile.DenyTools),
		WaitConditions:        normalizedSortedStrings(profile.WaitConditions),
		ProjectedFieldPaths:   projectedFieldPathsDigest(offered, in.ProjectedSchemas),
		CommandOnFloor:        commandOnFloor,
		Effects:               uint8(effects),
		RootCount:             in.RootCount,
		HasGit:                in.HasGit,
		HasAnyGitRoot:         in.HasGit || in.HasAnyGitRoot,
		OnWorkerBranch:        in.OnWorkerBranch,
		WebSearch:             in.WebSearch,
		SpawnAllowlist:        normalizedSortedStrings(in.SpawnAllowlist),
		ReviewsConfinedWrites: in.ReviewsConfinedWrites,
		CommandExternalHidden: in.CommandExternalHidden,
		SessionScratchDir:     strings.TrimSpace(in.SessionScratchDir),
		SharedTempLocations:   slices.Clone(in.SharedTempLocations),
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func projectedFieldPathsDigest(offered []string, projected map[string]map[string]any) map[string][]string {
	if len(projected) == 0 {
		return nil
	}
	out := make(map[string][]string, len(offered))
	for _, name := range offered {
		if schema, ok := projected[name]; ok && schema != nil {
			out[name] = toolschema.ArgFieldPaths(toolschema.ProjectArgsSchema(schema))
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
