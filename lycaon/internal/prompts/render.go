package prompts

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
)

// KicksTemplatePrefix identifies kick templates.
const KicksTemplatePrefix = "kicks/"

func validateSubRef(ref, prefix string) (string, error) {
	name := strings.TrimSpace(ref)
	if name == "" {
		return "", fmt.Errorf("empty template ref")
	}
	if sandbox.HasParentTraversal(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid template ref %q", ref)
	}
	if prefix != "" && !strings.HasPrefix(name, prefix) {
		return "", fmt.Errorf("template ref %q must use prefix %q", ref, prefix)
	}
	return name, nil
}

func mergeOccurrenceWithReader(reader Reader, data map[string]any) (map[string]any, error) {
	occurrence := make(map[string]any, len(data))
	for k, v := range data {
		if !IsDeclaredOrReservedReaderFact(k) {
			occurrence[k] = v
		}
	}
	return Vars(reader, occurrence)
}

// RenderInject executes inject/{ref}.md with a full DTO map.
func (e *FileTemplateEngine) RenderInject(ctx context.Context, ref string, data map[string]any) (string, error) {
	if e == nil {
		return "", fmt.Errorf("template engine not configured")
	}
	name, err := validateSubRef(ref, "")
	if err != nil {
		return "", err
	}
	path := InjectTemplatePrefix + name + ".md"
	body, err := e.Render(ctx, path, data)
	if err != nil {
		return "", err
	}
	return body, nil
}

// RenderGuidance executes guidance/{ref}.md with a full DTO map.
func (e *FileTemplateEngine) RenderGuidance(ctx context.Context, ref string, data map[string]any) (string, error) {
	if e == nil {
		return "", fmt.Errorf("template engine not configured")
	}
	name, err := validateSubRef(ref, "")
	if err != nil {
		return "", err
	}
	path := GuidanceTemplatePrefix + name + ".md"
	return e.Render(ctx, path, data)
}

// RenderKick executes kicks/{name}.md (name includes coordinator- or worker- prefix)
// against a validated Reader merged with occurrence data.
func (e *FileTemplateEngine) RenderKick(ctx context.Context, reader Reader, name string, data map[string]any) (string, error) {
	if e == nil {
		return "", fmt.Errorf("template engine not configured")
	}
	kickName, err := validateSubRef(name, "")
	if err != nil {
		return "", err
	}
	if !reader.Valid() {
		return "", fmt.Errorf("render kick %q requires a valid reader", kickName)
	}
	merged, err := mergeOccurrenceWithReader(reader, data)
	if err != nil {
		return "", err
	}
	if reader.ProfileID() == CoordinatorProfileID {
		spawn.RefreshSizingHintVars(merged)
	}
	path := KicksTemplatePrefix + kickName + ".md"
	return e.Render(ctx, path, merged)
}
