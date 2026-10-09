package prompts

import (
	"context"
	"fmt"

	"github.com/flosch/pongo2/v6"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"golang.org/x/sync/singleflight"
)

var compiledPrompts = scopedstore.New[*pongo2.Template](64)
var compilingPrompts singleflight.Group

// compiledTemplate reuses only immutable source graphs. Execution data and
// rendered output remain private to each call.
func (e *FileTemplateEngine) compiledTemplate(ctx context.Context, ref string) (*pongo2.Template, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.Revision() == "" {
		tpl, _, err := e.compileTemplate(ref)
		return tpl, err
	}
	key := e.Revision() + "\x00" + ref
	if tpl, ok := compiledPrompts.Load(key); ok {
		return tpl, nil
	}
	result := compilingPrompts.DoChan(key, func() (any, error) {
		if tpl, ok := compiledPrompts.Load(key); ok {
			return tpl, nil
		}
		tpl, size, err := e.compileTemplate(ref)
		if err == nil && size <= 256<<10 {
			compiledPrompts.Store(key, tpl)
		}
		return tpl, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return nil, result.Err
		}
		tpl, ok := result.Val.(*pongo2.Template)
		if !ok {
			return nil, fmt.Errorf("invalid compiled prompt")
		}
		return tpl, nil
	}
}

func (e *FileTemplateEngine) compileTemplate(ref string) (*pongo2.Template, int, error) {
	loader := newLayeredLoader(e.layers, e.registered)
	ref, err := loader.Preflight(ref)
	if err != nil {
		return nil, 0, err
	}
	// The compiled set needs only its preflighted graph, not the entire overlay.
	loader.layers, loader.registered = PromptLayers{}, nil
	size := 0
	for _, source := range loader.sources {
		size += len(source)
	}
	set, err := pongoplain.NewSet("lycaon-prompts", loader, pongoplain.Composed)
	if err != nil {
		return nil, 0, err
	}
	tpl, err := set.FromCache(ref)
	return tpl, size, err
}
