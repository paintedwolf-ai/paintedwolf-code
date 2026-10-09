package prompts

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/pongoplain"
)

// layeredLoader resolves one preflighted template graph.
type layeredLoader struct {
	layers     PromptLayers
	registered map[string]string
	sources    map[string]string
}

func newLayeredLoader(layers PromptLayers, registered map[string]string) *layeredLoader {
	return &layeredLoader{layers: layers, registered: registered, sources: make(map[string]string)}
}

// Abs keeps dependencies root-relative.
func (l *layeredLoader) Abs(_base, name string) string {
	ref, err := normalizeTemplateRef(name)
	if err != nil {
		return strings.TrimSpace(name)
	}
	return ref
}

func (l *layeredLoader) Get(path string) (io.Reader, error) {
	ref, err := normalizeTemplateRef(path)
	if err != nil {
		return nil, err
	}
	source, ok := l.sources[ref]
	if !ok {
		return nil, fmt.Errorf("%w: dependency %q was not preflighted", pongoplain.ErrComposition, ref)
	}
	return strings.NewReader(source), nil
}

// Preflight validates the static dependency graph.
func (l *layeredLoader) Preflight(root string) (string, error) {
	root, err := normalizeTemplateRef(root)
	if err != nil {
		return "", err
	}
	active := make(map[string]int)
	done := make(map[string]bool)
	stack := make([]string, 0, pongoplain.MaxCompositionDepth)

	var visit func(string) error
	visit = func(ref string) error {
		if done[ref] {
			return nil
		}
		if start, ok := active[ref]; ok {
			cycle := append(append([]string(nil), stack[start:]...), ref)
			return fmt.Errorf("%w: cycle %s", pongoplain.ErrComposition, strings.Join(cycle, " -> "))
		}
		if len(stack)+1 > pongoplain.MaxCompositionDepth {
			return fmt.Errorf("%w: depth exceeds %d at %q", pongoplain.ErrComposition, pongoplain.MaxCompositionDepth, ref)
		}
		if len(done)+len(active)+1 > pongoplain.MaxCompositionFiles {
			return fmt.Errorf("%w: graph exceeds %d files", pongoplain.ErrComposition, pongoplain.MaxCompositionFiles)
		}

		source, err := l.read(ref)
		if err != nil {
			return fmt.Errorf("template dependency %q: %w", ref, err)
		}
		analysis, err := pongoplain.Inspect(source, pongoplain.Composed)
		if err != nil {
			return fmt.Errorf("template dependency %q: %w", ref, err)
		}
		l.sources[ref] = source
		active[ref] = len(stack)
		stack = append(stack, ref)
		for _, dependency := range analysis.Dependencies {
			dependency, err = normalizeTemplateRef(dependency)
			if err != nil {
				return fmt.Errorf("template dependency from %q: %w", ref, err)
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		delete(active, ref)
		done[ref] = true
		return nil
	}
	if err := visit(root); err != nil {
		return "", err
	}
	return root, nil
}

func (l *layeredLoader) read(ref string) (string, error) {
	if source, ok := l.registered[ref]; ok {
		return source, nil
	}
	raw, err := l.layers.ReadFile(ref)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func normalizeTemplateRef(ref string) (string, error) {
	cleanRef := filepath.Clean(filepath.FromSlash(strings.TrimSpace(ref)))
	if cleanRef == "." || filepath.IsAbs(cleanRef) || cleanRef == ".." ||
		strings.HasPrefix(cleanRef, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("template path escapes prompts dir: %q", ref)
	}
	return filepath.ToSlash(cleanRef), nil
}
