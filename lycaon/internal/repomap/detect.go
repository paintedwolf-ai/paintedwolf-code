package repomap

import (
	"context"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/odvcencio/gotreesitter/grammars"
)

func detectGrammar(ctx context.Context, filename string, src []byte, mode filekind.DepthMode) *grammars.LangEntry {
	res := filekind.Detect(ctx, filekind.DetectReq{
		Filename:   filename,
		HeadSample: src,
		Mode:       mode,
	})
	return res.Grammar
}
