// Package summarize builds deterministic context packs.
package summarize

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/tokenest"
	"strings"

	"github.com/lycaon/lycaon/config"
)

// Caps is the summarize.yaml shape.
type Caps struct {
	Version int        `yaml:"version"`
	Gather  GatherCaps `yaml:"gather"`
	Anchors AnchorCaps `yaml:"anchors"`
	Pack    PackCaps   `yaml:"pack"`
}

// GatherCaps bounds how much material a single call pulls in.
type GatherCaps struct {
	FileReadBytes     int `yaml:"file_read_bytes"`
	MetadataNodes     int `yaml:"metadata_nodes"`
	IndexWaitMs       int `yaml:"index_wait_ms"`
	MaxBytes          int `yaml:"max_bytes"`
	FileChunkBytes    int `yaml:"file_chunk_bytes"`
	PatternPageFiles  int `yaml:"pattern_page_files"`
	MaxFilesRead      int `yaml:"max_files_read"`
	MaxListDepth      int `yaml:"max_list_depth"`
	FileHeadLines     int `yaml:"file_head_lines"`
	SymbolWindowLines int `yaml:"symbol_window_lines"` // substance window around a definition
	NeighborMax       int `yaml:"neighbor_max"`        // one-hop neighbor stubs; 0 disables
	CallSiteMax       int `yaml:"call_site_max"`       // call-site hit rows; 0 disables
	ImportEdgeMax     int `yaml:"import_edge_max"`     // inbound+outbound module edges; 0 disables
	InlineMaxBytes    int `yaml:"inline_max_bytes"`
	InlineMinBytes    int `yaml:"inline_min_bytes"`
	InlineChunkLines  int `yaml:"inline_chunk_lines"`
	// PatternMatchSampleMax bounds verbatim pattern samples.
	PatternMatchSampleMax int `yaml:"pattern_match_sample_max"`
	// PruneNestedVCS excludes nested repository roots.
	PruneNestedVCS bool `yaml:"prune_nested_vcs"`
}

// AnchorCaps bounds the anchor count returned to the agent.
type AnchorCaps struct {
	Default int `yaml:"default"`
	Max     int `yaml:"max"`
}

// PackCaps bounds pack assembly.
type PackCaps struct {
	InputBudgetTokens int `yaml:"input_budget_tokens"`
	// WireBudgetTokens bounds the complete response.
	WireBudgetTokens             int `yaml:"wire_budget_tokens"`
	SizeDivisor                  int `yaml:"size_divisor"`
	BreadthFractionPct           int `yaml:"breadth_fraction_pct"` // skeleton share before depth
	MultiPathIdentitySoftPaths   int `yaml:"multi_path_identity_soft_paths"`
	MultiPathIdentityFractionPct int `yaml:"multi_path_identity_fraction_pct"`
	// SubtreeFanoutMax bounds individually represented children.
	SubtreeFanoutMax int `yaml:"subtree_fanout_max"`
	// SubtreeDamping weights material→budget share: proportional | sqrt | log.
	SubtreeDamping string `yaml:"subtree_damping"`
	// SubtreeMinRollupSharePct reserves rollup coverage.
	SubtreeMinRollupSharePct int `yaml:"subtree_min_rollup_share_pct"`
	// SubtreeDoclinkMax bounds documentation tiebreaks.
	SubtreeDoclinkMax int `yaml:"subtree_doclink_max"`
	// SubtreeFaninMax bounds inbound-reference tiebreaks.
	SubtreeFaninMax int `yaml:"subtree_fanin_max"`
	// SubtreeFaninGrepMax bounds fan-in scans.
	SubtreeFaninGrepMax int `yaml:"subtree_fanin_grep_max"`
	// SubtreeMinDrills preserves depth in flat directories.
	SubtreeMinDrills int `yaml:"subtree_min_drills"`
	// SubtreeMaxDrills bounds detailed children per parent.
	SubtreeMaxDrills int `yaml:"subtree_max_drills"`
	// SubtreeTaskDepthAlpha weights task affinity.
	SubtreeTaskDepthAlpha float64 `yaml:"subtree_task_depth_alpha"`
	// SubtreeNameIndexMax bounds pre-drill name outlines.
	SubtreeNameIndexMax int `yaml:"subtree_name_index_max"`
	// NextActionsMax bounds returned follow-ups.
	NextActionsMax int `yaml:"next_actions_max"`
	// SubtreeSubstanceFloor adds one citable directory window.
	SubtreeSubstanceFloor bool `yaml:"subtree_substance_floor"`
}

// DefaultCaps returns the shipped summarize caps.
func DefaultCaps() Caps {
	caps, err := LoadCaps()
	if err != nil {
		panic(err)
	}
	return caps
}

// LoadCaps reads the shipped summarize caps.
func LoadCaps() (Caps, error) {
	raw, err := config.Read(config.Summarize)
	if err != nil {
		return Caps{}, fmt.Errorf("read summarize caps: %w", err)
	}
	var caps Caps
	if err := config.DecodeYAML(raw, &caps); err != nil {
		return Caps{}, fmt.Errorf("parse summarize caps: %w", err)
	}
	if caps.Version <= 0 {
		return Caps{}, fmt.Errorf("summarize caps: version must be positive")
	}
	caps.normalize()
	return caps, nil
}

// normalize enforces positive bounds.
func (c *Caps) normalize() {
	if c.Gather.FileReadBytes <= 0 {
		c.Gather.FileReadBytes = 1 << 20
	}
	if c.Gather.MetadataNodes <= 0 {
		c.Gather.MetadataNodes = 512
	}
	if c.Gather.IndexWaitMs < 0 {
		c.Gather.IndexWaitMs = 0
	}
	if c.Anchors.Default <= 0 {
		c.Anchors.Default = 1
	}
	if c.Anchors.Max <= 0 {
		c.Anchors.Max = c.Anchors.Default
	}
	if c.Anchors.Default > c.Anchors.Max {
		c.Anchors.Default = c.Anchors.Max
	}
	if c.Pack.SizeDivisor <= 0 {
		c.Pack.SizeDivisor = tokenest.DefaultDivisor
	}
	if c.Pack.InputBudgetTokens <= 0 {
		c.Pack.InputBudgetTokens = 1
	}
	if c.Gather.MaxBytes <= 0 {
		c.Gather.MaxBytes = 1
	}
	if c.Gather.FileChunkBytes <= 0 {
		c.Gather.FileChunkBytes = 1 << 20
	}
	if c.Gather.PatternPageFiles <= 0 {
		c.Gather.PatternPageFiles = 1
	}
	switch strings.TrimSpace(strings.ToLower(c.Pack.SubtreeDamping)) {
	case "proportional", "sqrt", "log", "":
		if c.Pack.SubtreeDamping == "" {
			c.Pack.SubtreeDamping = "sqrt"
		}
	default:
		c.Pack.SubtreeDamping = "sqrt"
	}
}

// ClampAnchors applies the configured default and maximum.
func (c Caps) ClampAnchors(requested int) int {
	if requested <= 0 {
		return c.Anchors.Default
	}
	if requested > c.Anchors.Max {
		return c.Anchors.Max
	}
	return requested
}
