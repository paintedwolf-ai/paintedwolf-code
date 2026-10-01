// Package sourceref carries source locations through model context projections.
package sourceref

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func Key(t api.NavigationTarget) string {
	return t.ProjectID + "\x00" + t.RootID + "\x00" + t.WorkerID + "\x00" + t.Path
}

// Merge preserves truncation from each input context.
func Merge(contexts ...*api.SourceContext) *api.SourceContext {
	out := &api.SourceContext{Locations: []api.NavigationTarget{}}
	seen := map[string]bool{}
	for _, context := range contexts {
		if context == nil {
			continue
		}
		out.Truncated = out.Truncated || context.Truncated
		for _, target := range context.Locations {
			key := Key(target)
			if !Valid(target) || seen[key] {
				continue
			}
			if len(out.Locations) == api.MaxSourceContextLocations {
				out.Truncated = true
				continue
			}
			seen[key] = true
			out.Locations = append(out.Locations, target)
		}
	}
	sort.Slice(out.Locations, func(i, j int) bool {
		a, b := out.Locations[i], out.Locations[j]
		if a.ProjectID != b.ProjectID {
			return a.ProjectID < b.ProjectID
		}
		if a.RootID != b.RootID {
			return a.RootID < b.RootID
		}
		if a.WorkerID != b.WorkerID {
			return a.WorkerID < b.WorkerID
		}
		return a.Path < b.Path
	})
	return out
}

func Valid(t api.NavigationTarget) bool {
	return t.ProjectID != "" && t.RootID != "" && t.Path != "" && len(t.Path) <= 4096 &&
		!strings.ContainsAny(t.Path, "\\\x00\r\n") && !strings.HasPrefix(t.Path, "/") &&
		path.Clean(t.Path) == t.Path && t.Path != ".." && !strings.HasPrefix(t.Path, "../") &&
		(t.EntryKind == api.NavigationEntryKindFile || t.EntryKind == api.NavigationEntryKindFolder)
}

var identifierRE = regexp.MustCompile(`[-\p{L}\p{N}_@./\\]+`)
var quotedRE = regexp.MustCompile("\"(?:[^\"\\\\]|\\\\.)*\"|`[^`\r\n]+`|<[^<>\r\n]+>")

type mentionSource struct {
	value  string
	quoted bool
}

type identifiers map[string][]mentionSource

func identify(content string) identifiers {
	out := identifiers{}
	add := func(value string, quoted bool) {
		value = strings.ReplaceAll(value, "\\", "/")
		value = strings.TrimRight(value, ".,")
		value = strings.TrimRight(value, "/")
		name := path.Base(value)
		out[name] = append(out[name], mentionSource{value: value, quoted: quoted})
	}
	for _, token := range identifierRE.FindAllString(content, -1) {
		add(token, false)
	}
	for _, quoted := range quotedRE.FindAllString(content, -1) {
		value := quoted[1 : len(quoted)-1]
		if quoted[0] == '"' {
			if decoded, err := strconv.Unquote(quoted); err == nil {
				value = decoded
			}
		}
		if address, ok := ParseLineAddress(value); ok && address.Path != "" {
			value = address.Path
		}
		add(value, true)
	}
	return out
}

func (names identifiers) retain(context *api.SourceContext) *api.SourceContext {
	out := &api.SourceContext{Locations: []api.NavigationTarget{}}
	if context == nil {
		return out
	}
	out.Truncated = context.Truncated
	for _, target := range context.Locations {
		isExtensionlessRoot := !strings.Contains(target.Path, "/") && !strings.Contains(path.Base(target.Path), ".")
		for _, token := range names[path.Base(target.Path)] {
			if isExtensionlessRoot && !token.quoted && !strings.HasPrefix(token.value, "./") && !strings.Contains(token.value, "/") {
				continue
			}
			if pathMentionMatches(token.value, target.Path) {
				out.Locations = append(out.Locations, target)
				break
			}
		}
	}
	return out
}

func pathMentionMatches(token, target string) bool {
	if strings.HasPrefix(token, "./") {
		return strings.TrimPrefix(token, "./") == target
	}
	if strings.HasPrefix(token, "@") {
		_, relative, qualified := strings.Cut(token, "/")
		return qualified && relative == target
	}
	if strings.HasPrefix(token, "/") {
		return strings.HasSuffix(token, "/"+target)
	}
	return token == target || strings.HasSuffix(target, "/"+token)
}

// Mentioned retains existing identities only; text cannot introduce a new location.
func Mentioned(context *api.SourceContext, content string) *api.SourceContext {
	if context == nil || len(context.Locations) == 0 {
		return (identifiers{}).retain(context)
	}
	return identify(content).retain(context)
}

// ForResponse selects identities present in both the response and fitted request
// before applying the location limit.
func ForResponse(messages []api.Message, response string) *api.SourceContext {
	names := identify(response)
	contexts := make([]*api.SourceContext, 0, len(messages))
	for _, message := range messages {
		selected := names.retain(message.SourceContext)
		if len(selected.Locations) > 0 {
			selected = Mentioned(selected, message.Content)
		}
		contexts = append(contexts, selected)
	}
	return Merge(contexts...)
}
