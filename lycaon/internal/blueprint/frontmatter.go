package blueprint

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

// frontmatterKeyOrder is the human-facing fence order. Remaining keys follow sorted.
var frontmatterKeyOrder = []string{"id", "status", "title", "updated_at", "research_depth"}

// identityNamespace derives ids for blueprint files that declare none.
var identityNamespace = uuid.MustParse("880638f7-4a5c-4d7f-a8b8-0be7b2d55bd1")

// ParseIDFrontmatter returns the UUID declared in YAML frontmatter, or "".
func ParseIDFrontmatter(content string) string {
	fm, _ := blueprintfile.SplitMarkdownFrontmatter(content)
	if fm == nil {
		return ""
	}
	raw, ok := fm["id"]
	if !ok || raw == nil {
		return ""
	}
	id, err := uuid.Parse(strings.TrimSpace(fmt.Sprint(raw)))
	if err != nil {
		return ""
	}
	return id.String()
}

// Identity is the blueprint's id: the declared frontmatter id, else one derived
// from its project and path. Reads never write; the next host write of the file
// persists the derived id so it survives a later move.
func Identity(projectID, path, content string) string {
	if id := ParseIDFrontmatter(content); id != "" {
		return id
	}
	return uuid.NewSHA1(identityNamespace, []byte(projectID+"\x00"+path)).String()
}

// EnsureIDFrontmatter sets id in frontmatter, preserving body.
func EnsureIDFrontmatter(content, id string) string {
	id = strings.TrimSpace(id)
	meta, body := blueprintfile.SplitMarkdownFrontmatter(content)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["id"] = id
	return wrapFrontmatter(encodeFrontmatter(meta), body)
}

// ParseStatusFrontmatter returns status from YAML frontmatter (default draft).
// Missing or corrupt frontmatter → draft.
func ParseStatusFrontmatter(content string) api.BlueprintStatus {
	fm, _ := blueprintfile.SplitMarkdownFrontmatter(content)
	if fm == nil {
		return api.BlueprintStatusDraft
	}
	raw, ok := fm["status"]
	if !ok || raw == nil {
		return api.BlueprintStatusDraft
	}
	s := strings.TrimSpace(fmt.Sprint(raw))
	switch api.BlueprintStatus(s) {
	case api.BlueprintStatusApproved, api.BlueprintStatusImplementing, api.BlueprintStatusDone:
		return api.BlueprintStatus(s)
	default:
		return api.BlueprintStatusDraft
	}
}

// ParseTitleFrontmatter returns title from frontmatter when present.
func ParseTitleFrontmatter(content string) string {
	fm, _ := blueprintfile.SplitMarkdownFrontmatter(content)
	if fm == nil {
		return ""
	}
	raw, ok := fm["title"]
	if !ok || raw == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

// ParseUpdatedAtFrontmatter returns the declared updated_at; files without one have no update time.
func ParseUpdatedAtFrontmatter(content string) (time.Time, bool) {
	fm, _ := blueprintfile.SplitMarkdownFrontmatter(content)
	if fm == nil {
		return time.Time{}, false
	}
	raw, ok := fm["updated_at"]
	if !ok || raw == nil {
		return time.Time{}, false
	}
	switch v := raw.(type) {
	case time.Time:
		return v.UTC(), true
	case string:
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(v)); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// SetUpdatedAtFrontmatter sets or replaces updated_at in YAML frontmatter.
func SetUpdatedAtFrontmatter(content string, t time.Time) string {
	meta, body := blueprintfile.SplitMarkdownFrontmatter(content)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["updated_at"] = t.UTC().Format(time.RFC3339)
	return wrapFrontmatter(encodeFrontmatter(meta), body)
}

// SetStatusFrontmatter sets or replaces status in YAML frontmatter.
// Missing or corrupt frontmatter is replaced with a minimal clean block; body is preserved.
func SetStatusFrontmatter(content string, status api.BlueprintStatus) string {
	status = normalizeStatus(status)
	meta, body := blueprintfile.SplitMarkdownFrontmatter(content)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["status"] = string(status)
	return wrapFrontmatter(encodeFrontmatter(meta), body)
}

// ResetToDraftFrontmatter forces status draft (launch seed / new run).
func ResetToDraftFrontmatter(content string) string {
	return SetStatusFrontmatter(content, api.BlueprintStatusDraft)
}

// EnsureTitleFrontmatter sets title in frontmatter, preserving body and repairing corrupt fences.
func EnsureTitleFrontmatter(content, title string) string {
	title = strings.TrimSpace(title)
	meta, body := blueprintfile.SplitMarkdownFrontmatter(content)
	if meta == nil {
		meta = map[string]any{}
	}
	if _, ok := meta["status"]; !ok {
		meta["status"] = string(api.BlueprintStatusDraft)
	}
	meta["title"] = title
	return wrapFrontmatter(encodeFrontmatter(meta), body)
}

func wrapFrontmatter(fence, body string) string {
	fence = strings.TrimSpace(fence)
	if fence == "" {
		return body
	}
	return "---\n" + fence + "\n---\n" + body
}

func encodeFrontmatter(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	written := make(map[string]bool, len(meta))
	var b strings.Builder
	write := func(key string) {
		raw, ok := meta[key]
		if !ok {
			return
		}
		written[key] = true
		b.WriteString(yamlLine(key, raw))
	}
	for _, key := range frontmatterKeyOrder {
		write(key)
	}
	var rest []string
	for key := range meta {
		if !written[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		write(key)
	}
	return strings.TrimSpace(b.String())
}

func yamlLine(key string, value any) string {
	raw, err := yaml.Marshal(map[string]any{key: value})
	if err != nil {
		return key + ": " + yamlQuote(fmt.Sprint(value)) + "\n"
	}
	return string(raw)
}

func yamlQuote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return `""`
	}
	return fmt.Sprintf("%q", s)
}

func normalizeStatus(status api.BlueprintStatus) api.BlueprintStatus {
	switch status {
	case api.BlueprintStatusApproved, api.BlueprintStatusImplementing, api.BlueprintStatusDone:
		return status
	default:
		return api.BlueprintStatusDraft
	}
}
