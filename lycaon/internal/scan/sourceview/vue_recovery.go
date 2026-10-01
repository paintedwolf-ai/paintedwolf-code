package sourceview

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
	"golang.org/x/net/html"
)

func parseVueComponent(ctx context.Context, source []byte, grammar *gotreesitter.Language) (*gotreesitter.Tree, []Limitation, error) {
	tree, err := tsparse.Parse(ctx, grammar, source, tsparse.Analysis)
	if err != nil {
		return nil, nil, err
	}
	if validVueTree(tree, len(source)) {
		return tree, nil, nil
	}
	if tree != nil {
		tree.Release()
	}
	limitation := &Limitation{Construct: "vue_sfc", Detail: "Vue component parsing is incomplete"}
	masked, limitations := maskVueTemplates(source)
	if len(limitations) == 0 {
		return nil, nil, limitation
	}
	tree, err = tsparse.Parse(ctx, grammar, masked, tsparse.Analysis)
	if err != nil {
		return nil, nil, err
	}
	if !validVueTree(tree, len(source)) {
		if tree != nil {
			tree.Release()
		}
		return nil, nil, limitation
	}
	return tree, limitations, nil
}

func validVueTree(tree *gotreesitter.Tree, length int) bool {
	if tree == nil || tree.RootNode() == nil {
		return false
	}
	root := tree.RootNode()
	return !root.HasError() && !hasSourceRecovery(root) && int(root.EndByte()) == length
}

// Complete template bodies are masked before validating component boundaries.
func maskVueTemplates(source []byte) ([]byte, []Limitation) {
	masked := bytes.Clone(source)
	originalLines := lineOffsets(source)
	var limitations []Limitation
	tokenizer := html.NewTokenizer(bytes.NewReader(source))
	position := 0
	for {
		kind := tokenizer.Next()
		start := position
		position += len(tokenizer.Raw())
		switch kind {
		case html.ErrorToken:
			if errors.Is(tokenizer.Err(), io.EOF) {
				return masked, limitations
			}
			return nil, nil
		case html.CommentToken:
			continue
		case html.TextToken:
			if len(bytes.TrimSpace(tokenizer.Raw())) == 0 {
				continue
			}
			return nil, nil
		case html.SelfClosingTagToken:
			continue
		case html.StartTagToken:
			name := tokenizer.Token().Data
			body := position
			end, ok := consumeVueBlock(tokenizer, name, &position)
			if !ok {
				return nil, nil
			}
			if name == "template" {
				for i := body; i < end; i++ {
					if masked[i] != '\n' && masked[i] != '\r' {
						masked[i] = ' '
					}
				}
				limitations = append(limitations, Limitation{Construct: "vue_sfc", Detail: "Vue template parsing is incomplete", Start: sourcePosition(start, originalLines)})
			}
		default:
			return nil, nil
		}
	}
}

func consumeVueBlock(tokenizer *html.Tokenizer, name string, position *int) (int, bool) {
	depth := 1
	for {
		kind := tokenizer.Next()
		start := *position
		*position += len(tokenizer.Raw())
		if kind == html.ErrorToken {
			return 0, false
		}
		if tokenizer.Token().Data != name {
			continue
		}
		switch kind {
		case html.StartTagToken:
			depth++
		case html.EndTagToken:
			depth--
			if depth == 0 {
				return start, true
			}
		default:
		}
	}
}
