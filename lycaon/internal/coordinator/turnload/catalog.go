// Package turnload selects additional tools and omittable instructions from model scores.
// Engine failures preserve the surface floor and all instructions.
package turnload

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
)

// Turn kinds the kind question can answer.
const (
	KindAnswerOnly = "answer_only"
	KindInspect    = "inspect"
	KindChange     = "change"
	KindRun        = "run"
	KindDelegate   = "delegate"
)

// Version is the decisions.yaml format this package reads.
const Version = 2

// StateSpec bounds the state every decision reads.
type StateSpec struct {
	UserTextChars int `yaml:"user_text_chars" json:"user_text_chars"`
	RecentTools   int `yaml:"recent_tools" json:"recent_tools"`
	// HeadTokens bounds each encoded question; the state receives the remaining context.
	HeadTokens int `yaml:"head_tokens" json:"head_tokens"`
}

// ToolsSpec declares how loadable tool schemas are scored: one multi question
// whose options are the loadable tools, each answered as its own yes/no.
type ToolsSpec struct {
	// Independent encodes every option of the turn's multi questions, tools
	// and guides alike, on its own row, so a head's answer for one option
	// never depends on the rest of the roster.
	Independent bool `yaml:"independent" json:"independent,omitempty"`
	// Options are the release's validated preload vocabulary, separate from open retrieval.
	Options map[string]string `yaml:"-" json:"options,omitempty"`
	// Question is the instruction the options answer.
	Question string `yaml:"question" json:"question"`
	// OptionWords bounds each tool's option text after its name.
	OptionWords int     `yaml:"option_words" json:"option_words"`
	LoadAt      float64 `yaml:"load_at" json:"load_at"`
}

// GuidesSpec declares how instruction units are scored: one multi question
// whose options are the units.
type GuidesSpec struct {
	// Omittable lists units whose removal was calibrated for the installed head.
	// An empty list keeps every unit. Other scores remain diagnostic.
	Omittable []string `yaml:"omittable" json:"omittable"`

	Question    string `yaml:"question" json:"question"`
	OptionWords int    `yaml:"option_words" json:"option_words"`
	// OmitBelow is the P(true) under which a confident unit is omitted; 0 keeps
	// every unit.
	OmitBelow       float64 `yaml:"omit_below" json:"omit_below"`
	ConfidenceFloor float64 `yaml:"confidence_floor" json:"confidence_floor"`
}

// KindSpec declares the turn-kind choice.
type KindSpec struct {
	// VetoTools lets a confident answer_only kind withhold every loadable tool.
	VetoTools       bool              `yaml:"veto_tools" json:"veto_tools"`
	ConfidenceFloor float64           `yaml:"confidence_floor" json:"confidence_floor"`
	Instructions    string            `yaml:"instructions" json:"instructions"`
	Options         map[string]string `yaml:"options" json:"options"`
}

// SkillPreloadSpec controls automatic selection of one procedure at turn
// start: the top skill is read when it scores at least preload_at and leads
// the runner-up by margin. Zero preload_at disables the read.
type SkillPreloadSpec struct {
	PreloadAt float64 `yaml:"preload_at" json:"preload_at"`
	Margin    float64 `yaml:"margin" json:"margin"`
}

// ToolEventSpec controls the skill read a turn takes when it first calls a
// loadable tool: the roster is ranked against the request and that tool,
// the top skill is read at read_at and pointed to at pointer_at, both with
// margin over the runner-up. Tools in skip never trigger it.
type ToolEventSpec struct {
	DeadlineMS int      `yaml:"deadline_ms" json:"deadline_ms"`
	ReadAt     float64  `yaml:"read_at" json:"read_at"`
	PointerAt  float64  `yaml:"pointer_at" json:"pointer_at"`
	Margin     float64  `yaml:"margin" json:"margin"`
	Skip       []string `yaml:"skip" json:"skip,omitempty"`
}

// Deadline returns the engine budget for a tool-event ranking.
func (s ToolEventSpec) Deadline() time.Duration {
	return time.Duration(s.DeadlineMS) * time.Millisecond
}

// Skips reports whether a tool's first call never triggers a skill read.
func (s ToolEventSpec) Skips(tool string) bool {
	return slices.Contains(s.Skip, strings.TrimSpace(tool))
}

// TurnSpec is the per-turn question set.
type TurnSpec struct {
	DeadlineMS int              `yaml:"deadline_ms" json:"deadline_ms"`
	Tools      ToolsSpec        `yaml:"tools" json:"tools"`
	Skills     SkillPreloadSpec `yaml:"skills" json:"skills"`
	Guides     GuidesSpec       `yaml:"guides" json:"guides"`
	Kind       KindSpec         `yaml:"kind" json:"kind"`
}

// Deadline returns the engine budget for the turn call.
func (s TurnSpec) Deadline() time.Duration { return time.Duration(s.DeadlineMS) * time.Millisecond }

// RequestSpec declares how request_tools text is matched to schemas.
type RequestSpec struct {
	DeadlineMS int     `yaml:"deadline_ms" json:"deadline_ms"`
	LoadAt     float64 `yaml:"load_at" json:"load_at"`
	MaxLoads   int     `yaml:"max_loads" json:"max_loads"`
	// NearestLoads is how many of the engine's closest tools load when it
	// answers and none reaches load_at.
	NearestLoads int `yaml:"nearest_loads" json:"nearest_loads"`
}

// Deadline returns the engine budget for a request ranking.
func (s RequestSpec) Deadline() time.Duration { return time.Duration(s.DeadlineMS) * time.Millisecond }

// LookupSpec declares how skills_read text is matched to skills: the top
// skill is read when it scores at least read_at and leads the runner-up by
// margin.
type LookupSpec struct {
	DeadlineMS int     `yaml:"deadline_ms" json:"deadline_ms"`
	ReadAt     float64 `yaml:"read_at" json:"read_at"`
	Margin     float64 `yaml:"margin" json:"margin"`
}

// Deadline returns the engine budget for a skill lookup.
func (s LookupSpec) Deadline() time.Duration { return time.Duration(s.DeadlineMS) * time.Millisecond }

// Catalog is the validated decision catalog.
type Catalog struct {
	Version   int           `yaml:"version" json:"version"`
	State     StateSpec     `yaml:"state" json:"state"`
	Turn      TurnSpec      `yaml:"turn" json:"turn"`
	Request   RequestSpec   `yaml:"request" json:"request"`
	Lookup    LookupSpec    `yaml:"lookup" json:"lookup"`
	ToolEvent ToolEventSpec `yaml:"tool_event" json:"tool_event"`
	// Rerank belongs to internal/decide, which parses the same file; it is
	// named here so a strict decode accepts it.
	Rerank any `yaml:"rerank" json:"-"`
}

var decoded struct {
	mu      sync.Mutex
	raw     []byte
	release []byte
	catalog Catalog
}

// LoadCatalog reads and validates decisions.yaml.
func LoadCatalog() (Catalog, error) {
	data, err := config.Read(config.Decisions)
	if err != nil {
		return Catalog{}, fmt.Errorf("read decisions: %w", err)
	}
	release, err := config.Read(config.DecisionRelease)
	if err != nil {
		return Catalog{}, fmt.Errorf("read decision release: %w", err)
	}
	decoded.mu.Lock()
	defer decoded.mu.Unlock()
	if decoded.raw != nil && bytes.Equal(decoded.raw, data) && bytes.Equal(decoded.release, release) {
		return decoded.catalog, nil
	}
	cat, err := ParseCatalog(data)
	if err != nil {
		return Catalog{}, err
	}
	var artifact struct {
		Preload struct {
			Options     map[string]string `json:"options"`
			OptionWords int               `json:"option_words"`
			HeadTokens  int               `json:"head_tokens"`
			Encoding    string            `json:"encoding"`
		} `json:"preload"`
	}
	if err := json.Unmarshal(release, &artifact); err != nil {
		return Catalog{}, fmt.Errorf("parse decision release: %w", err)
	}
	if len(artifact.Preload.Options) == 0 {
		return Catalog{}, fmt.Errorf("decision release has no preload vocabulary")
	}
	encoding := "joint"
	if cat.Turn.Tools.Independent {
		encoding = "independent"
	}
	if artifact.Preload.Encoding != encoding || cat.Turn.Tools.OptionWords != artifact.Preload.OptionWords || cat.State.HeadTokens != artifact.Preload.HeadTokens {
		return Catalog{}, fmt.Errorf("decision encoding differs from the release vocabulary")
	}
	cat.Turn.Tools.Options = artifact.Preload.Options
	decoded.release = append([]byte(nil), release...)
	decoded.raw = append([]byte(nil), data...)
	decoded.catalog = cat
	return cat, nil
}

// ParseCatalog decodes and validates one decisions.yaml document.
func ParseCatalog(data []byte) (Catalog, error) {
	var cat Catalog
	if err := config.DecodeYAML(data, &cat); err != nil {
		return Catalog{}, fmt.Errorf("parse decisions: %w", err)
	}
	if err := cat.validate(); err != nil {
		return Catalog{}, err
	}
	return cat, nil
}

func (c Catalog) validate() error {
	var faults []string
	fault := func(format string, args ...any) { faults = append(faults, fmt.Sprintf(format, args...)) }
	if c.Version != Version {
		fault("version %d is not %d", c.Version, Version)
	}
	if c.State.UserTextChars <= 0 {
		fault("state.user_text_chars must be positive")
	}
	if c.State.RecentTools < 0 {
		fault("state.recent_tools must not be negative")
	}
	if c.Turn.DeadlineMS <= 0 {
		fault("turn.deadline_ms must be positive")
	}
	if c.State.HeadTokens <= 0 {
		fault("state.head_tokens must be positive")
	}
	if strings.TrimSpace(c.Turn.Tools.Question) == "" {
		fault("turn.tools.question is required")
	}
	if c.Turn.Tools.OptionWords <= 0 {
		fault("turn.tools.option_words must be positive")
	}
	if !unit(c.Turn.Tools.LoadAt) {
		fault("turn.tools.load_at must be in (0,1]")
	}
	if c.Turn.Tools.Independent && c.Turn.Kind.VetoTools {
		fault("independent heads answer no kind question, so the kind veto must be off")
	}
	if strings.TrimSpace(c.Turn.Guides.Question) == "" {
		fault("turn.guides.question is required")
	}
	if c.Turn.Guides.OptionWords <= 0 {
		fault("turn.guides.option_words must be positive")
	}
	if c.Turn.Guides.OmitBelow < 0 || c.Turn.Guides.OmitBelow > 1 {
		fault("turn.guides.omit_below must be in [0,1]")
	}
	if !unit(c.Turn.Guides.ConfidenceFloor) {
		fault("turn.guides.confidence_floor must be in (0,1]")
	}
	if strings.TrimSpace(c.Turn.Kind.Instructions) == "" {
		fault("turn.kind.instructions is required")
	}
	for _, kind := range Kinds() {
		if _, ok := c.Turn.Kind.Options[kind]; !ok {
			fault("turn.kind.options lacks %s", kind)
		}
	}
	for option := range c.Turn.Kind.Options {
		if !knownKind(option) {
			fault("turn.kind.options has unknown option %s", option)
		}
	}
	if !unit(c.Turn.Kind.ConfidenceFloor) {
		fault("turn.kind.confidence_floor must be in (0,1]")
	}
	if c.Request.DeadlineMS <= 0 {
		fault("request.deadline_ms must be positive")
	}
	if c.Request.LoadAt <= 0 || c.Request.LoadAt > RankScoreCeiling {
		fault("request.load_at must be in (0,%v]", RankScoreCeiling)
	}
	if c.Request.MaxLoads <= 0 {
		fault("request.max_loads must be positive")
	}
	if c.Request.NearestLoads < 0 || c.Request.NearestLoads > c.Request.MaxLoads {
		fault("request.nearest_loads must be in [0, max_loads]")
	}
	if !(c.Turn.Skills.PreloadAt >= 0 && c.Turn.Skills.PreloadAt <= RankScoreCeiling) {
		fault("turn.skills.preload_at must be in [0,%v]", RankScoreCeiling)
	}
	if !rankMargin(c.Turn.Skills.Margin) {
		fault("turn.skills.margin must be in [0,%v]", RankScoreCeiling)
	}
	if c.Lookup.DeadlineMS <= 0 {
		fault("lookup.deadline_ms must be positive")
	}
	if c.Lookup.ReadAt <= 0 || c.Lookup.ReadAt > RankScoreCeiling {
		fault("lookup.read_at must be in (0,%v]", RankScoreCeiling)
	}
	if !rankMargin(c.Lookup.Margin) {
		fault("lookup.margin must be in [0,%v]", RankScoreCeiling)
	}
	if c.ToolEvent.DeadlineMS <= 0 {
		fault("tool_event.deadline_ms must be positive")
	}
	if !(c.ToolEvent.ReadAt >= 0 && c.ToolEvent.ReadAt <= RankScoreCeiling) {
		fault("tool_event.read_at must be in [0,%v]", RankScoreCeiling)
	}
	if !(c.ToolEvent.PointerAt >= 0 && c.ToolEvent.PointerAt <= c.ToolEvent.ReadAt) {
		fault("tool_event.pointer_at must be in [0, read_at]")
	}
	if !rankMargin(c.ToolEvent.Margin) {
		fault("tool_event.margin must be in [0,%v]", RankScoreCeiling)
	}
	for _, tool := range c.ToolEvent.Skip {
		if strings.TrimSpace(tool) == "" {
			fault("tool_event.skip contains an empty name")
		}
	}
	if len(faults) > 0 {
		sort.Strings(faults)
		return fmt.Errorf("decisions.yaml: %s", strings.Join(faults, "; "))
	}
	return nil
}

func unit(v float64) bool { return v > 0 && v <= 1 }

func rankMargin(v float64) bool { return v >= 0 && v <= RankScoreCeiling }

// Kinds lists every turn kind.
func Kinds() []string {
	return []string{KindAnswerOnly, KindInspect, KindChange, KindRun, KindDelegate}
}

func knownKind(kind string) bool {
	for _, k := range Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// RankScoreCeiling is the top of the engine's relevance rubric.
const RankScoreCeiling = 4.0
