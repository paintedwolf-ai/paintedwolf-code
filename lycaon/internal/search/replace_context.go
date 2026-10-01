package search

import (
	"context"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/pkg/api"
)

const contextHeadSampleBytes = 512

// classifyReplaceContexts labels each hunk from its syntax node.
func classifyReplaceContexts(ctx context.Context, path, content string, hunks []ReplaceHunk) {
	if len(hunks) == 0 {
		return
	}
	head := content
	if len(head) > contextHeadSampleBytes {
		head = head[:contextHeadSampleBytes]
	}
	res := filekind.Detect(ctx, filekind.DetectReq{
		Filename:   path,
		HeadSample: []byte(head),
		Mode:       filekind.DepthShallow,
	})
	if res.Grammar == nil {
		return
	}
	lang := res.Grammar.Language()
	if lang == nil {
		return
	}
	tree, err := tsparse.Parse(ctx, lang, []byte(content), tsparse.Analysis)
	if err != nil || tree == nil {
		return
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		return
	}
	for i := range hunks {
		h := &hunks[i]
		if h.start < 0 || h.start >= len(content) || h.start > math.MaxUint32 {
			continue
		}
		probe := uint32(h.start)
		node := root.NamedDescendantForByteRange(probe, probe)
		for n := node; n != nil; n = n.Parent() {
			if isCommentOrStringNodeType(n.Type(lang)) {
				h.Context = api.SearchReplaceContextCommentOrString
				break
			}
		}
	}
}

// isCommentOrStringNodeType recognizes syntax node families.
func isCommentOrStringNodeType(nodeType string) bool {
	t := strings.ToLower(nodeType)
	return strings.Contains(t, "comment") ||
		strings.Contains(t, "string") ||
		strings.Contains(t, "char_literal") ||
		strings.Contains(t, "heredoc")
}
