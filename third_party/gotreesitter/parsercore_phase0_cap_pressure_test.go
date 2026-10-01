//go:build !gts_no_parsercorephase0

package gotreesitter

import "testing"

func TestDiagnosticParserCoreProjectedNodesRoundsUp(t *testing.T) {
	tests := []struct {
		name                          string
		nodes, progress, source, want uint32
	}{
		{name: "exact", nodes: 128, progress: 100, source: 1000, want: 1280},
		{name: "rounded", nodes: 100, progress: 333, source: 1000, want: 301},
		{name: "zero progress", nodes: 100, source: 1000, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := diagnosticParserCoreProjectedNodes(test.nodes, test.progress, test.source); got != uint64(test.want) {
				t.Fatalf("projected nodes = %d, want %d", got, test.want)
			}
		})
	}
}

func TestDiagnosticParserCoreCapPressureRequiresTwoHighSamples(t *testing.T) {
	const maxNodes = uint32(1024)
	tests := []struct {
		name           string
		firstProgress  uint32
		secondProgress uint32
		wantDecline    bool
	}{
		{name: "stable cap pressure", firstProgress: 100, secondProgress: 200, wantDecline: true},
		{name: "early pressure clears", firstProgress: 100, secondProgress: 256},
		{name: "late pressure only", firstProgress: 128, secondProgress: 200},
		{name: "stable route", firstProgress: 160, secondProgress: 320},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var prediction diagnosticParserCoreCapPressurePrediction
			decline, _ := prediction.observe(maxNodes/8, test.firstProgress, maxNodes, maxNodes)
			if decline {
				t.Fatal("first sample declined")
			}
			decline, _ = prediction.observe(maxNodes/4, test.secondProgress, maxNodes, maxNodes)
			if decline != test.wantDecline {
				t.Fatalf("decline = %t, want %t", decline, test.wantDecline)
			}
		})
	}
}

func TestDiagnosticParserCoreCapPressureSourceFloorDoesNotSample(t *testing.T) {
	const maxNodes = uint32(1024)
	var prediction diagnosticParserCoreCapPressurePrediction
	decline, projected := prediction.observe(maxNodes/4, 1, maxNodes/8-1, maxNodes)
	if decline || projected != 0 || prediction.samples != 0 {
		t.Fatalf("short source changed prediction: decline=%t projected=%d samples=%d", decline, projected, prediction.samples)
	}
}
