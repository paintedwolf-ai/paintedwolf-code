package modelcall

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

func TestClassifyTurn(t *testing.T) {
	toolsPresent := []tools.ToolMeta{{Name: "update_progress"}}

	tests := []struct {
		name string
		req  CompletionRequest
		want TurnClass
	}{
		{
			name: "no tools open",
			req:  CompletionRequest{Debug: RequestDebug{Surface: toolcontract.SurfaceImplementDispatch}},
			want: TurnClassOpen,
		},
		{
			name: "dispatch orchestration",
			req: CompletionRequest{
				Tools: toolsPresent,
				Debug: RequestDebug{Surface: toolcontract.SurfaceImplementDispatch},
			},
			want: TurnClassOrchestration,
		},
		{
			name: "investigate orchestration",
			req: CompletionRequest{
				Tools: toolsPresent,
				Debug: RequestDebug{Surface: toolcontract.SurfaceImplementInvestigate},
			},
			want: TurnClassOrchestration,
		},
		{
			name: "synthesis open",
			req: CompletionRequest{
				Tools: toolsPresent,
				Debug: RequestDebug{Surface: "implement_synthesis"},
			},
			want: TurnClassOpen,
		},
		{
			name: "worker tool turn",
			req: CompletionRequest{
				Tools: toolsPresent,
				Debug: RequestDebug{ProfileID: "implementer"},
			},
			want: TurnClassOrchestration,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurn(tc.req); got != tc.want {
				t.Fatalf("ClassifyTurn() = %q want %q", got, tc.want)
			}
		})
	}
}
