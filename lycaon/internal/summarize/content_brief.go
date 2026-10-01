package summarize

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/fileoutline"
)

// ContentInput is revision-pinned source plus its shared structural analysis.
type ContentInput struct {
	Path     string
	Content  string
	Task     string
	Analysis fileoutline.Result
}

// BriefContent builds a ContextPack from revision-pinned bytes.
func BriefContent(ctx context.Context, input ContentInput, caps Caps) (Result, error) {
	outline := input.Analysis
	structure := StructureCandidate{
		RelPath:       input.Path,
		Kind:          StructureKindFile,
		LineCount:     outline.TotalLines,
		Head:          input.Content,
		StartLine:     1,
		ContentHash:   HashString(input.Content),
		Language:      outline.Language,
		OutlineSource: outline.Source,
		Parses:        outline.Parses,
	}
	if structure.LineCount <= 0 {
		structure.LineCount = strings.Count(input.Content, "\n") + 1
	}
	for _, symbol := range outline.Symbols {
		structure.Symbols = append(structure.Symbols, StructureSymbol{
			Kind: symbol.Kind,
			Name: symbol.Name,
			Line: symbol.Line,
		})
	}
	gather := contentBriefGatherer{result: GatherResult{
		Mode:      ModeRepo,
		Structure: []StructureCandidate{structure},
		Stats: GatherStats{
			Mode:         ModeRepo,
			Candidates:   1,
			PathIsFile:   true,
			UseStructure: true,
		},
		Bytes: len(input.Content),
	}}
	engine := NewEngine(gather, caps)
	return engine.Run(ctx, Request{
		Task:       input.Task,
		Path:       input.Path,
		MaxAnchors: caps.ClampAnchors(0),
	})
}

type contentBriefGatherer struct{ result GatherResult }

func (g contentBriefGatherer) Gather(context.Context, Request) (GatherResult, error) {
	return g.result, nil
}
