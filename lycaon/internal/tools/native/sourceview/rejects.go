package sourceview

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/pkg/api"
)

func PathNotFound(tool, relPath, fullPath string) error {
	dir := filepath.Dir(fullPath)
	base := filepath.Base(fullPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return &tools.ToolReject{
			Code: "READ_PATH_NOT_FOUND",
			Data: map[string]any{"path": relPath, "tool": tool},
		}
	}
	var suggestions []string
	lowerBase := strings.ToLower(base)
	baseStem := strings.TrimSuffix(lowerBase, filepath.Ext(lowerBase))
	for _, e := range entries {
		name := e.Name()
		lower := strings.ToLower(name)
		stem := strings.TrimSuffix(lower, filepath.Ext(lower))
		if strings.Contains(lower, lowerBase) || strings.Contains(lowerBase, lower) ||
			(baseStem != "" && (strings.Contains(stem, baseStem) || strings.Contains(baseStem, stem))) {
			suggestions = append(suggestions, filepath.Join(filepath.Dir(relPath), name))
		}
	}
	if len(suggestions) > 3 {
		suggestions = suggestions[:3]
	}
	data := map[string]any{"path": relPath, "tool": tool}
	if len(suggestions) > 0 {
		data["suggestions"] = suggestions
	}
	return &tools.ToolReject{Code: "READ_PATH_NOT_FOUND", Data: data}
}

func PatternInvalid(path string, err error) error {
	return safecmd.Reject("STRUCTURAL_PATTERN_INVALID", map[string]any{"path": path, "reason": err.Error()})
}
func LanguageUnknown(path, lang string) error {
	return safecmd.Reject("STRUCTURAL_LANG_UNKNOWN", map[string]any{"path": path, "detail": fmt.Sprintf("unknown grammar %q", lang), "structural_languages": filekind.SupportedLanguages()})
}

func ParseReject(path, phase string, err error) *tools.ToolReject {
	var failure *tsparse.Failure
	if !errors.As(err, &failure) {
		return nil
	}
	var pattern *structrewrite.PatternError
	if errors.As(err, &pattern) {
		phase = "pattern"
	}
	data := failure.Facts(phase)
	data["path"] = path
	code := "SOURCE_PARSE_FAILED"
	if failure.Incomplete() {
		code = "SOURCE_PARSE_INCOMPLETE"
	}
	return &tools.ToolReject{Code: code, Data: data}
}

func ReportParseFailures(tctx tools.ToolContext, failures []tsparse.FileFailure, total int) {
	if tctx.Out == nil || len(failures) == 0 {
		return
	}
	first := failures[0]
	data := first.Failure.Facts(first.Phase)
	data["path"], data["parse_failure_count"] = first.Path, total
	paths := make([]string, len(failures))
	for i, failure := range failures {
		paths[i] = failure.Path
	}
	data["parse_paths"] = paths
	tctx.Out.Facts = tctx.Out.Facts.WithFeedback(tools.SourceAnalysisUnavailableCode, data, &api.FeedbackSubject{Kind: "path", ID: first.Path})
}
