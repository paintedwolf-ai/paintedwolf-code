package repomap

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	_ "github.com/lycaon/lycaon/internal/repomap/gtsqueries"
)

// TagsFromBytes returns bounded definition tags for source bytes.
func TagsFromBytes(ctx context.Context, relHint string, src []byte) FileTagOutcome {
	if len(src) > DefaultWalkMaxFileBytes {
		return FileTagOutcome{Skip: SkipStats{Oversized: 1}}
	}
	entry := detectGrammar(ctx, filepath.Base(strings.TrimSpace(relHint)), src, filekind.DepthDeep)
	if entry == nil {
		return FileTagOutcome{Skip: SkipStats{NoGrammar: 1}}
	}
	return tagSourceWithGrammar(ctx, relHint, src, *entry, &taggerCache{})
}
