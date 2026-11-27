package transcript

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"slices"
	"sort"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

var turnLoadWireCache = scopedstore.New[api.TurnLoad](256)

func turnLoadWireCacheKey(r store.TurnLoadReceipt) string {
	h := sha256.New()
	writePart := func(s string) {
		var lenBuf [8]byte
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(s)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(s))
	}
	writePart(r.SessionID)
	writePart(r.Trigger)
	writePart(r.OpeningMessageID)
	writePart(r.ToolCallID)
	writePart(r.Reason)
	writePart(r.Decisions)
	writePart(r.Standing)
	var meta [9]byte
	if r.Abstained {
		meta[0] = 1
	}
	binary.LittleEndian.PutUint64(meta[1:], uint64(max(r.ElapsedMs, 0)))
	_, _ = h.Write(meta[:])
	return string(h.Sum(nil))
}

func cloneTurnLoad(in api.TurnLoad) api.TurnLoad {
	out := in
	out.Floor = slices.Clone(in.Floor)
	out.Tools = slices.Clone(in.Tools)
	if in.Engine != nil {
		cp := *in.Engine
		out.Engine = &cp
	}
	if in.PreloadedSkill != nil {
		cp := *in.PreloadedSkill
		out.PreloadedSkill = &cp
	}
	if in.Kind != nil {
		cp := *in.Kind
		out.Kind = &cp
	}
	if in.Boundary != nil {
		cp := *in.Boundary
		out.Boundary = &cp
	}
	if in.Match != nil {
		cp := *in.Match
		cp.Names = slices.Clone(in.Match.Names)
		out.Match = &cp
	}
	return out
}

// receiptDecisions is the decisions_json a receipt carries, one key per trigger.
type receiptDecisions struct {
	Skills         *turnload.Ranking          `json:"skills"`
	PreloadedSkill *turnload.SkillRank        `json:"preloaded_skill"`
	Turn           *turnload.Decision         `json:"turn"`
	Request        *turnload.RequestOutcome   `json:"request"`
	Lookup         *turnload.LookupOutcome    `json:"lookup"`
	ToolEvent      *turnload.ToolEventOutcome `json:"tool_event"`
	// Floor names the tools the surface offers on every call.
	Floor []string `json:"floor"`
	// Boundary is the prompt-cache state the turn opened on.
	Boundary *turnload.Boundary `json:"boundary"`
}

// TurnLoadWire projects one receipt to the transcript's shape. The page and
// the turn_load event both go through it, so a reload draws what the event
// drew.
func TurnLoadWire(r store.TurnLoadReceipt) api.TurnLoad {
	cacheKey := turnLoadWireCacheKey(r)
	if cached, ok := turnLoadWireCache.Load(cacheKey); ok {
		return cloneTurnLoad(cached)
	}
	out := api.TurnLoad{
		SessionID:        r.SessionID,
		Trigger:          api.TurnLoadTrigger(r.Trigger),
		OpeningMessageID: r.OpeningMessageID,
		ToolCallID:       r.ToolCallID,
		Abstained:        r.Abstained,
		Reason:           r.Reason,
		ElapsedMs:        max(r.ElapsedMs, 0),
		Floor:            []string{},
		Tools:            []api.TurnLoadTool{},
	}
	var decisions receiptDecisions
	_ = json.Unmarshal([]byte(r.Decisions), &decisions)
	out.Engine = wireEngine(decisions)
	switch r.Trigger {
	case store.TurnLoadTriggerTurn, store.TurnLoadTriggerVisibility:
		if p := decisions.PreloadedSkill; p != nil {
			out.PreloadedSkill = &api.TurnLoadSkill{Name: p.Name, Score: p.Score}
		}
		if len(decisions.Floor) > 0 {
			out.Floor = append(out.Floor, decisions.Floor...)
			sort.Strings(out.Floor)
		}
		var standing turnload.Standing
		if json.Unmarshal([]byte(r.Standing), &standing) == nil {
			out.Tools = wireTools(standing, r.OpeningMessageID)
		}
		if d := decisions.Turn; d != nil {
			out.Kind = wireKind(*d)
		}
		if b := decisions.Boundary; b != nil {
			out.Boundary = &api.TurnLoadBoundary{
				Cache:       api.TurnLoadCacheState(b.Cache),
				Reason:      api.TurnLoadColdReason(b.Reason),
				IdleMs:      b.IdleMS,
				ColdAfterMs: b.ColdAfterMS,
			}
		}
	case store.TurnLoadTriggerRequest:
		if o := decisions.Request; o != nil {
			out.Match = &api.TurnLoadMatch{Need: o.Need, By: requestMatchBy(*o), Names: o.Loaded()}
		}
	case store.TurnLoadTriggerLookup:
		if o := decisions.Lookup; o != nil {
			out.Match = &api.TurnLoadMatch{Need: o.Need, By: lookupMatchBy(*o), Names: o.Names()}
		}
	case store.TurnLoadTriggerToolEvent:
		if o := decisions.ToolEvent; o != nil {
			out.Match = &api.TurnLoadMatch{Need: o.Tool, By: toolEventMatchBy(*o), Names: o.Names()}
		}
		if p := decisions.PreloadedSkill; p != nil {
			out.PreloadedSkill = &api.TurnLoadSkill{Name: p.Name, Score: p.Score}
		}
	}
	if out.Match != nil && out.Match.Names == nil {
		out.Match.Names = []string{}
	}
	turnLoadWireCache.Store(cacheKey, out)
	return cloneTurnLoad(out)
}

// wireEngine is the first engine that answered any of the receipt's decisions.
func wireEngine(decisions receiptDecisions) *api.TurnLoadEngine {
	candidates := []decide.Engine{}
	if decisions.Turn != nil {
		candidates = append(candidates, decisions.Turn.Engine)
	}
	if decisions.Skills != nil {
		candidates = append(candidates, decisions.Skills.Engine)
	}
	if decisions.Request != nil {
		candidates = append(candidates, decisions.Request.Engine)
	}
	if decisions.Lookup != nil {
		candidates = append(candidates, decisions.Lookup.Engine)
	}
	if decisions.ToolEvent != nil {
		candidates = append(candidates, decisions.ToolEvent.Engine)
	}
	for _, e := range candidates {
		if e.Name != "" {
			return &api.TurnLoadEngine{Name: e.Name, Model: e.Model, Head: e.Head, Label: EngineLabel(e)}
		}
	}
	return nil
}

func wireKind(d turnload.Decision) *api.TurnLoadKind {
	if d.Kind == "" {
		return nil
	}
	return &api.TurnLoadKind{Value: d.Kind, Confidence: d.KindConfidence}
}

// wireTools lists the standing tools, sorted by name; a tool that joined on
// another turn than openingMessageID was carried.
func wireTools(standing turnload.Standing, openingMessageID string) []api.TurnLoadTool {
	out := make([]api.TurnLoadTool, 0, len(standing.Tools))
	for _, load := range standing.Tools {
		out = append(out, api.TurnLoadTool{
			Tool:    load.Tool,
			Source:  api.TurnLoadToolSource(load.Source),
			P:       load.P,
			Need:    load.Need,
			With:    load.With,
			Carried: load.Turn != openingMessageID,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

func requestMatchBy(o turnload.RequestOutcome) api.TurnLoadMatchBy {
	switch {
	case len(o.Ranked) > 0:
		return api.TurnLoadMatchByEngine
	case len(o.Exact) > 0:
		return api.TurnLoadMatchByName
	default:
		return api.TurnLoadMatchByNone
	}
}

func toolEventMatchBy(o turnload.ToolEventOutcome) api.TurnLoadMatchBy {
	if o.Read != nil || o.Pointer != nil {
		return api.TurnLoadMatchByEngine
	}
	return api.TurnLoadMatchByNone
}

func lookupMatchBy(o turnload.LookupOutcome) api.TurnLoadMatchBy {
	switch {
	case o.Ranked != nil:
		return api.TurnLoadMatchByEngine
	case o.Exact != "":
		return api.TurnLoadMatchByName
	default:
		return api.TurnLoadMatchByNone
	}
}

func EngineLabel(engines ...decide.Engine) string {
	for _, e := range engines {
		if e.Name != "" {
			label := e.Name + "/" + e.Model
			if e.Head != "" {
				label += "#" + e.Head
			}
			return label
		}
	}
	return ""
}
