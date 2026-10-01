package compaction

import (
	"context"
	"strings"
	"testing"
)

// Echoed host markers do not select the overlay trimmer.
func TestCompactChunkRoutesOverlayPromoteByToolIdentity(t *testing.T) {
	cc := NewChunkCompactor(CompactionConfig{ChunkTargetTokens: 400, ChunkTokenThreshold: 10}, nil)

	// summarize output that echoes promote sentinels out of the repo.
	echo := `{"task":"what is this repo","brief":["the OVERLAY_PROMOTE_SPILL hint and BANNER_PROMOTE_CONFLICT_DIGEST live here"],"anchors":[{"path":"x.go","line":1,"excerpt":"OVERLAY_PROMOTE_SPILL"}]}`
	out, meta, err := cc.CompactChunk(context.Background(), ContentChunk{Class: testChunkDiet("summarize", ""), Kind: ChunkKindToolResult, ToolName: "summarize"}, echo)
	if err != nil {
		t.Fatalf("compact summarize: %v", err)
	}
	if meta.Strategy == "overlay_promote_preserve" {
		t.Fatalf("summarize output misrouted to the overlay-promote preserver")
	}
	if strings.Contains(out, "compacted overlay promote") || strings.Contains(out, `"job_id"`) {
		t.Fatalf("summarize output mangled into an overlay-promote payload:\n%s", out)
	}

	// The host stamp selects structural preservation.
	promote := `{"job_id":"j1","mode":"promote","conflict_digest":[{"path":"a.go","conflict_tier":"tier1"}]}`
	_, meta2, err := cc.CompactChunk(context.Background(), ContentChunk{
		Kind:     ChunkKindToolResult,
		ToolName: "promote_overlay",
		Class:    testChunkDiet("promote_overlay", DietStampSourceOverlayMerge),
	}, promote)
	if err != nil {
		t.Fatalf("compact promote: %v", err)
	}
	if meta2.Strategy != "overlay_promote_preserve" {
		t.Fatalf("promote_overlay result strategy = %q want overlay_promote_preserve", meta2.Strategy)
	}
}
