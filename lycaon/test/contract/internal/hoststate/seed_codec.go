package hoststate

import (
	"errors"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/gate"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/pkg/api"
)

// Seed bytes select each domain member by index modulo the domain size, so
// every byte string decodes and every member is reachable from one byte.
const (
	SeedCodecVersion uint8 = 1
	SeedByteLength   int   = 21
	maxByteDomain    int   = 256
)

const (
	byteVersion = iota
	byteRecipient
	byteOverlay
	bytePosture
	byteMode
	byteSurfaceOrAgent
	byteRootsOrBranch
	byteLoading
	byteWorkflowOrRootCount
	byteApproval
	byteTempOrUpgraded
	byteMCP
	byteFlags
	byteFamily
	byteItemLow
	byteChangeCount
	byteChange1Axis
	byteChange1Value
	byteChange2Axis
	byteChange2Value
	byteItemHigh
)

const (
	flagWebSearch = 1 << iota
	flagVision
	flagExternalHidden
	flagSecondaryRootGit
)

// workerRootCounts are the worker root counts in byte order; zero comes last
// so the one- and two-root bytes keep their meaning.
var workerRootCounts = []int{1, 2, 0}

// SeedChange is one turn-B mutation selector; the fuzz runner interprets it.
type SeedChange struct {
	Axis, Value uint8
}

// SeedTail carries the case selectors that follow the state coordinates.
type SeedTail struct {
	Family      uint8
	Item        uint16
	ChangeCount uint8
	Changes     [2]SeedChange
}

// SeedCase is one decoded fuzz input. Worker is false for a coordinator case.
// WorkerRoute is false when the selected overlay has no worker agents.
type SeedCase struct {
	Worker      bool
	WorkerRoute bool
	Coordinator CoordinatorSeed
	WorkerSeed  WorkerSeed
	Tail        SeedTail
}

// Validate rejects a domain the codec cannot represent: an empty required
// domain or one larger than a byte selects. An overlay without worker agents
// is valid and omits the worker route.
func (d *SeedDomains) Validate() error {
	if d == nil {
		return fmt.Errorf("seed domains required")
	}
	for _, dom := range []struct {
		name string
		size int
	}{
		{"overlays", len(d.Overlays)},
		{"session postures", len(d.Postures)},
		{"surfaces", len(d.Surfaces)},
		{"session modes", len(d.SessionModes)},
		{"workflow states", len(d.WorkflowStates)},
		{"root topologies", len(d.RootTopologies)},
		{"loading modes", len(d.LoadingModes)},
		{"approval postures", len(d.ApprovalPostures)},
		{"temp states", len(d.TempStates)},
		{"MCP states", len(d.MCPStates)},
		{"worker branches", len(d.WorkerBranches)},
		{"worker loadings", len(d.WorkerLoadings)},
	} {
		if err := checkByteDomain(dom.name, dom.size); err != nil {
			return err
		}
	}
	for _, dom := range []struct {
		name string
		keys []string
	}{
		{"overlays", keysOf(d.Overlays, func(o PackOverlay) string { return o.ID })},
		{"session postures", keysOf(d.Postures, func(p sessionprofiles.PostureSpec) string { return string(p.ID) })},
		{"surfaces", keysOf(d.Surfaces, func(s SurfaceInfo) string { return s.ID })},
		{"session modes", keysOf(d.SessionModes, func(m api.SessionMode) string { return string(m) })},
		{"workflow states", keysOf(d.WorkflowStates, func(w WorkflowState) string { return string(w) })},
		{"root topologies", keysOf(d.RootTopologies, func(r RootTopology) string { return string(r) })},
		{"loading modes", keysOf(d.LoadingModes, func(l LoadingMode) string { return string(l) })},
		{"approval postures", keysOf(d.ApprovalPostures, func(p gate.Posture) string { return string(p) })},
		{"temp states", keysOf(d.TempStates, func(t TempState) string { return string(t) })},
		{"MCP states", keysOf(d.MCPStates, func(m MCPState) string { return string(m) })},
		{"worker branches", keysOf(d.WorkerBranches, func(b WorkerBranchView) string { return string(b) })},
		{"worker loadings", keysOf(d.WorkerLoadings, func(l WorkerLoadingMode) string { return string(l) })},
	} {
		seen := map[string]bool{}
		for _, k := range dom.keys {
			if k == "" {
				return fmt.Errorf("seed domain %s has an unset member", dom.name)
			}
			if seen[k] {
				return fmt.Errorf("seed domain %s lists %s twice; only the first is selectable", dom.name, k)
			}
			seen[k] = true
		}
	}
	for _, ov := range d.Overlays {
		if len(ov.WorkerAgents) > maxByteDomain {
			return fmt.Errorf("overlay %s has %d worker agents; a seed byte selects at most %d", ov.ID, len(ov.WorkerAgents), maxByteDomain)
		}
	}
	return nil
}

func keysOf[T any](values []T, key func(T) string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = key(v)
	}
	return out
}

func checkByteDomain(name string, size int) error {
	switch {
	case size == 0:
		return fmt.Errorf("required seed domain %s is empty", name)
	case size > maxByteDomain:
		return fmt.Errorf("seed domain %s has %d members; a seed byte selects at most %d", name, size, maxByteDomain)
	}
	return nil
}

func memberIndex[T any](domain string, values []T, want any, match func(T) bool) (uint8, error) {
	i := slices.IndexFunc(values, match)
	if i < 0 {
		return 0, fmt.Errorf("seed domain %s has no member %v", domain, want)
	}
	return uint8(i), nil
}

func comparableIndex[T comparable](domain string, values []T, want T) (uint8, error) {
	return memberIndex(domain, values, want, func(v T) bool { return v == want })
}

func overlayIndex(d *SeedDomains, idx int) (uint8, error) {
	if idx < 0 || idx >= len(d.Overlays) {
		return 0, fmt.Errorf("overlay index %d outside %d overlays", idx, len(d.Overlays))
	}
	return uint8(idx), nil
}

func (t SeedTail) put(b []byte) {
	b[byteFamily] = t.Family
	b[byteItemLow] = uint8(t.Item)
	b[byteItemHigh] = uint8(t.Item >> 8)
	b[byteChangeCount] = t.ChangeCount
	b[byteChange1Axis], b[byteChange1Value] = t.Changes[0].Axis, t.Changes[0].Value
	b[byteChange2Axis], b[byteChange2Value] = t.Changes[1].Axis, t.Changes[1].Value
}

func boolFlag(on bool, flag uint8) uint8 {
	if on {
		return flag
	}
	return 0
}

// EncodeCoordinator encodes seed; every coordinate must name a domain member.
func (d *SeedDomains) EncodeCoordinator(seed CoordinatorSeed, tail SeedTail) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	b := make([]byte, SeedByteLength)
	b[byteVersion] = SeedCodecVersion
	var errs []error
	idx := func(v uint8, err error) uint8 {
		errs = append(errs, err)
		return v
	}
	b[byteOverlay] = idx(overlayIndex(d, seed.OverlayIndex))
	b[bytePosture] = idx(memberIndex("session postures", d.Postures, seed.Posture, func(p sessionprofiles.PostureSpec) bool { return p.ID == seed.Posture }))
	b[byteMode] = idx(comparableIndex("session modes", d.SessionModes, seed.SessionMode))
	b[byteSurfaceOrAgent] = idx(memberIndex("surfaces", d.Surfaces, seed.SurfaceID, func(s SurfaceInfo) bool { return s.ID == seed.SurfaceID }))
	b[byteRootsOrBranch] = idx(comparableIndex("root topologies", d.RootTopologies, seed.RootTopology))
	b[byteLoading] = idx(comparableIndex("loading modes", d.LoadingModes, seed.Loading))
	b[byteWorkflowOrRootCount] = idx(comparableIndex("workflow states", d.WorkflowStates, seed.WorkflowState))
	b[byteApproval] = idx(comparableIndex("approval postures", d.ApprovalPostures, seed.ApprovalPosture))
	b[byteTempOrUpgraded] = idx(comparableIndex("temp states", d.TempStates, seed.TempState))
	b[byteMCP] = idx(comparableIndex("MCP states", d.MCPStates, seed.MCPState))
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	b[byteFlags] = boolFlag(seed.WebSearch, flagWebSearch) | boolFlag(seed.Vision, flagVision) | boolFlag(seed.CommandExternalHidden, flagExternalHidden)
	tail.put(b)
	return b, nil
}

// EncodeWorker encodes seed; every coordinate must name a domain member.
func (d *SeedDomains) EncodeWorker(seed WorkerSeed, tail SeedTail) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	ov, err := overlayIndex(d, seed.OverlayIndex)
	if err != nil {
		return nil, err
	}
	rootByte := slices.Index(workerRootCounts, seed.RootCount)
	if rootByte < 0 {
		return nil, fmt.Errorf("worker root count %d is not one of %v", seed.RootCount, workerRootCounts)
	}
	if seed.SecondaryRootGit && seed.RootCount != 2 {
		return nil, fmt.Errorf("a secondary Git root needs two roots")
	}
	if seed.ParentUpgradedAskToAgent && seed.ParentSessionMode != api.SessionModeAsk {
		return nil, fmt.Errorf("only an Ask parent can upgrade")
	}
	b := make([]byte, SeedByteLength)
	b[byteVersion] = SeedCodecVersion
	b[byteRecipient] = 1
	b[byteOverlay] = ov
	var errs []error
	idx := func(v uint8, err error) uint8 {
		errs = append(errs, err)
		return v
	}
	agents := d.Overlays[ov].WorkerAgents
	b[byteSurfaceOrAgent] = idx(memberIndex("overlay "+d.Overlays[ov].ID+" worker agents", agents, seed.AgentID, func(a WorkerAgent) bool { return a.ID == seed.AgentID }))
	b[bytePosture] = idx(memberIndex("session postures", d.Postures, seed.Posture, func(p sessionprofiles.PostureSpec) bool { return p.ID == seed.Posture }))
	b[byteMode] = idx(comparableIndex("session modes", d.SessionModes, seed.ParentSessionMode))
	b[byteRootsOrBranch] = idx(comparableIndex("worker branches", d.WorkerBranches, seed.BranchView))
	b[byteLoading] = idx(comparableIndex("worker loadings", d.WorkerLoadings, seed.Loading))
	b[byteApproval] = idx(comparableIndex("approval postures", d.ApprovalPostures, seed.ApprovalPosture))
	b[byteMCP] = idx(comparableIndex("temp states", d.TempStates, seed.TempState))
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	b[byteWorkflowOrRootCount] = uint8(rootByte)
	b[byteTempOrUpgraded] = boolFlag(seed.ParentUpgradedAskToAgent, 1)
	b[byteFlags] = boolFlag(seed.WebSearch, flagWebSearch) | boolFlag(seed.Vision, flagVision) |
		boolFlag(seed.CommandExternalHidden, flagExternalHidden) | boolFlag(seed.SecondaryRootGit, flagSecondaryRootGit)
	tail.put(b)
	return b, nil
}

// Decode selects a case from arbitrary bytes. Missing bytes read as zero and
// bytes past SeedByteLength are ignored.
func (d *SeedDomains) Decode(data []byte) (SeedCase, error) {
	if err := d.Validate(); err != nil {
		return SeedCase{}, err
	}
	at := func(i int) uint8 {
		if i < len(data) {
			return data[i]
		}
		return 0
	}
	pick := func(i, size int) int { return int(at(i)) % size }
	c := SeedCase{
		Worker: at(byteRecipient)%2 == 1,
		Tail: SeedTail{
			Family:      at(byteFamily),
			Item:        uint16(at(byteItemLow)) | uint16(at(byteItemHigh))<<8,
			ChangeCount: at(byteChangeCount),
			Changes: [2]SeedChange{
				{at(byteChange1Axis), at(byteChange1Value)},
				{at(byteChange2Axis), at(byteChange2Value)},
			},
		},
	}
	ov := pick(byteOverlay, len(d.Overlays))
	flags := at(byteFlags)
	posture := d.Postures[pick(bytePosture, len(d.Postures))].ID
	mode := d.SessionModes[pick(byteMode, len(d.SessionModes))]
	approval := d.ApprovalPostures[pick(byteApproval, len(d.ApprovalPostures))]
	if !c.Worker {
		c.Coordinator = CoordinatorSeed{
			OverlayIndex:          ov,
			SessionMode:           mode,
			Posture:               posture,
			WorkflowState:         d.WorkflowStates[pick(byteWorkflowOrRootCount, len(d.WorkflowStates))],
			SurfaceID:             d.Surfaces[pick(byteSurfaceOrAgent, len(d.Surfaces))].ID,
			RootTopology:          d.RootTopologies[pick(byteRootsOrBranch, len(d.RootTopologies))],
			Loading:               d.LoadingModes[pick(byteLoading, len(d.LoadingModes))],
			WebSearch:             flags&flagWebSearch != 0,
			Vision:                flags&flagVision != 0,
			CommandExternalHidden: flags&flagExternalHidden != 0,
			ApprovalPosture:       approval,
			TempState:             d.TempStates[pick(byteTempOrUpgraded, len(d.TempStates))],
			MCPState:              d.MCPStates[pick(byteMCP, len(d.MCPStates))],
		}
		return c, nil
	}
	agents := d.Overlays[ov].WorkerAgents
	if len(agents) == 0 {
		c.WorkerSeed = WorkerSeed{OverlayIndex: ov}
		return c, nil
	}
	c.WorkerRoute = true
	rootCount := workerRootCounts[pick(byteWorkflowOrRootCount, len(workerRootCounts))]
	c.WorkerSeed = WorkerSeed{
		OverlayIndex:             ov,
		AgentID:                  agents[pick(byteSurfaceOrAgent, len(agents))].ID,
		Posture:                  posture,
		BranchView:               d.WorkerBranches[pick(byteRootsOrBranch, len(d.WorkerBranches))],
		RootCount:                rootCount,
		SecondaryRootGit:         rootCount == 2 && flags&flagSecondaryRootGit != 0,
		Loading:                  d.WorkerLoadings[pick(byteLoading, len(d.WorkerLoadings))],
		ParentSessionMode:        mode,
		ParentUpgradedAskToAgent: mode == api.SessionModeAsk && at(byteTempOrUpgraded)%2 == 1,
		WebSearch:                flags&flagWebSearch != 0,
		Vision:                   flags&flagVision != 0,
		CommandExternalHidden:    flags&flagExternalHidden != 0,
		ApprovalPosture:          approval,
		TempState:                d.TempStates[pick(byteMCP, len(d.TempStates))],
	}
	return c, nil
}
