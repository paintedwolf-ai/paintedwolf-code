package prompts

import (
	"context"
	"fmt"

	"github.com/flosch/pongo2/v6"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"golang.org/x/sync/singleflight"
)

type compiledPromptEntry struct {
	tpl        *pongo2.Template
	provenance []UnitProvenanceRecord
}

var compiledPrompts = scopedstore.New[compiledPromptEntry](64)
var compilingPrompts singleflight.Group

// compiledTemplate reuses only immutable source graphs. Execution data and
// rendered output remain private to each call.
func (e *FileTemplateEngine) compiledTemplate(ctx context.Context, ref string) (*pongo2.Template, error) {
	entry, err := e.compiledTemplateEntry(ctx, ref)
	if err != nil {
		return nil, err
	}
	return entry.tpl, nil
}

func (e *FileTemplateEngine) compiledTemplateEntry(ctx context.Context, ref string) (compiledPromptEntry, error) {
	if err := ctx.Err(); err != nil {
		return compiledPromptEntry{}, err
	}
	if e.Revision() == "" {
		entry, _, err := e.compileTemplate(ref)
		return entry, err
	}
	key := e.Revision() + "\x00" + ref
	if entry, ok := compiledPrompts.Load(key); ok {
		return entry, nil
	}
	result := compilingPrompts.DoChan(key, func() (any, error) {
		if entry, ok := compiledPrompts.Load(key); ok {
			return entry, nil
		}
		entry, size, err := e.compileTemplate(ref)
		if err == nil && size <= 256<<10 {
			compiledPrompts.Store(key, entry)
		}
		return entry, err
	})
	select {
	case <-ctx.Done():
		return compiledPromptEntry{}, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return compiledPromptEntry{}, result.Err
		}
		entry, ok := result.Val.(compiledPromptEntry)
		if !ok {
			return compiledPromptEntry{}, fmt.Errorf("invalid compiled prompt")
		}
		return entry, nil
	}
}

func (e *FileTemplateEngine) compileTemplate(ref string) (compiledPromptEntry, int, error) {
	loader := newLayeredLoader(e.layers, e.registered)
	ref, err := loader.Preflight(ref)
	if err != nil {
		return compiledPromptEntry{}, 0, err
	}
	prov := loader.Provenance()
	// The compiled set needs only its preflighted graph, not the entire overlay.
	loader.layers, loader.registered = PromptLayers{}, nil
	size := 0
	for _, source := range loader.sources {
		size += len(source)
	}
	set, err := pongoplain.NewSet("lycaon-prompts", loader, pongoplain.Composed)
	if err != nil {
		return compiledPromptEntry{}, 0, err
	}
	tpl, err := set.FromCache(ref)
	if err != nil {
		return compiledPromptEntry{}, 0, err
	}
	return compiledPromptEntry{tpl: tpl, provenance: prov}, size, nil
}
