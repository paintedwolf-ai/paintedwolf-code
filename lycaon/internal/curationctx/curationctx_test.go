package curationctx_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
)

func TestTaskHintRoundTrip(t *testing.T) {
	ctx := curationctx.WithTaskHint(context.Background(), "audit altitude")
	if got := curationctx.TaskHint(ctx); got != "audit altitude" {
		t.Fatalf("hint = %q", got)
	}
}

func TestTaskHintTruncates(t *testing.T) {
	long := strings.Repeat("x", 400)
	got := curationctx.TaskHint(curationctx.WithTaskHint(context.Background(), long))
	if len([]rune(got)) > 281 {
		t.Fatalf("truncated len = %d", len([]rune(got)))
	}
}

func TestLaneOccupancyRoundTrip(t *testing.T) {
	if curationctx.LaneOccupied(context.Background()) {
		t.Fatal("empty ctx occupied the lane")
	}
	occupied := curationctx.WithLane(context.Background())
	if !curationctx.LaneOccupied(occupied) {
		t.Fatal("WithLane did not occupy")
	}
	if curationctx.LaneOccupied(curationctx.WithoutLane(occupied)) {
		t.Fatal("WithoutLane left occupancy set")
	}
}

func TestTruncateTaskHint(t *testing.T) {
	if got := curationctx.TruncateTaskHint("  hello  "); got != "hello" {
		t.Fatalf("got = %q", got)
	}
	long := strings.Repeat("x", 400)
	got := curationctx.TruncateTaskHint(long)
	if len([]rune(got)) > 281 {
		t.Fatalf("truncated len = %d", len([]rune(got)))
	}
}
