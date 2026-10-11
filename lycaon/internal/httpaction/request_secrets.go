package httpaction

import (
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
)

// requestArgumentEvidence screens before host serialization can change the bytes.
func requestArgumentEvidence(ctx context.Context, matcher *secretmatch.Matcher, args map[string]any, resolution *secretcap.Resolution) ([]secretmatch.Match, error) {
	known, err := resolution.Matches(matcher, func(path string) bool { return secretmatch.HTTPArgumentConsumed(args, path) })
	if err != nil {
		return nil, err
	}
	var detected []secretmatch.Match
	walkRequestValues(args, "", "", func(path, label, value string) string {
		if secretmatch.HTTPArgumentConsumed(args, path) {
			matches := matcher.ScreenLabeledContext(ctx, requestFieldLabel(args, path, label), value)
			detected = append(detected, resolution.UnattributedMatches(value, matches, func(path string) bool { return secretmatch.HTTPArgumentConsumed(args, path) })...)
		}
		return value
	})
	return secretmatch.MergeEvidence(known, detected), nil
}

// redactRequestArguments retains local path inputs and redacts protocol fields.
func redactRequestArguments(ctx context.Context, matcher *secretmatch.Matcher, args map[string]any, tc tools.ToolContext) map[string]any {
	return walkRequestValues(args, "", "", func(path, label, value string) string {
		if !secretmatch.HTTPArgumentConsumed(args, path) {
			return value
		}
		value = tc.Effects.Secrets.RedactArgument(path, value)
		return matcher.RedactLabeled(ctx, requestFieldLabel(args, path, label), value)
	}).(map[string]any)
}

func requestFieldLabel(args map[string]any, path, fallback string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 3 || parts[2] != "value" || (parts[0] != "headers" && parts[0] != "form" && parts[0] != "body_form") {
		return fallback
	}
	items, _ := args[parts[0]].([]any)
	i, err := strconv.Atoi(parts[1])
	if err != nil || i < 0 || i >= len(items) {
		return fallback
	}
	item, _ := items[i].(map[string]any)
	label, _ := item["name"].(string)
	return label
}

func walkRequestValues(value any, path, label string, visit func(string, string, string) string) any {
	switch v := value.(type) {
	case string:
		return visit(path, label, v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			out[key] = walkRequestValues(child, path+"/"+escaped, key, visit)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = walkRequestValues(child, path+"/"+strconv.Itoa(i), label, visit)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, child := range v {
			out[i] = visit(path+"/"+strconv.Itoa(i), label, child)
		}
		return out
	default:
		return v
	}
}

// requestFiles freezes uploaded bytes across review and redaction reconstruction.
type requestFiles map[string]requestFile
type requestFile struct {
	bytes   []byte
	display string
}
type requestFilesKey struct{}

func withRequestFiles(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestFilesKey{}, requestFiles{})
}
