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
	ProfileID      string
	SurfaceID      string
	Mode           string
	Profiles       []sandbox.ToolProfile
	Plan           toolsurface.Plan
	Loaded         map[string]bool
	Visible        []string
	Schemas        *toolschema.Config
	RootCount      int
	HasGit         bool
	OnWorkerBranch bool
	WebSearch      bool
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
		row, err := cat.Surface(surfaceID)
		if err != nil {
			return Reader{}, err
		}
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

	toolSet := make(map[string]bool, len(offered)+len(requestable))
	for _, name := range offered {
		toolSet[name] = true
	}
	for _, name := range requestable {
		toolSet[name] = true
	}
	held := readerHeldSet(toolSet, effects)
	mints := evidenceMints(held)
	mode := strings.TrimSpace(in.Mode)
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
		fingerprint: computeReaderFingerprint(host, profileID, surfaceID, mode, offered, requestable, commandOnFloor, effects, in),
	}, nil
}

func readerHeldSet(tools map[string]bool, effects toolcontract.CommandEffects) map[string]bool {
	held := make(map[string]bool, len(tools)+3)
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
	return held
}

// Valid reports whether r was constructed by NewReader.
func (r Reader) Valid() bool { return r.profileID != "" }

// Host returns the prompt unit host (coordinator or worker).
func (r Reader) Host() promptunit.Host { return r.selection.Host }

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

// Held returns the reach set: offered and requestable tools plus command reach marks.
func (r Reader) Held() map[string]bool { return maps.Clone(r.held) }

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
	profileID, surfaceID, mode string,
	offered, requestable []string,
	commandOnFloor bool,
	effects toolcontract.CommandEffects,
	in ReaderInput,
) string {
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
		Host:           host,
		ProfileID:      profileID,
		SurfaceID:      surfaceID,
		Mode:           mode,
		Offered:        offered,
		Requestable:    requestable,
		CommandOnFloor: commandOnFloor,
		Effects:        uint8(effects),
		RootCount:      in.RootCount,
		HasGit:         in.HasGit,
		OnWorkerBranch: in.OnWorkerBranch,
		WebSearch:      in.WebSearch,
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
