package native

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

type rewriteBlocked struct {
	Path    string         `json:"path"`
	Code    string         `json:"code"`
	Details map[string]any `json:"details,omitempty"`
}

type multiApplyFileOut struct {
	SyntaxIssue *rewriteBlocked `json:"syntax_issue,omitempty"`
	Path        string          `json:"path"`
	Language    string          `json:"language"`
	Matches     int             `json:"matches"`
	Diff        string          `json:"diff,omitempty"`
	ParseError  string          `json:"parse_error,omitempty"`
	// InEditor marks a rewrite that landed in the person's open document;
	// Unsaved marks one the document holds but disk does not, because disk
	// could not receive the accepted document edits.
	InEditor  bool   `json:"in_editor,omitempty"`
	Unsaved   bool   `json:"unsaved,omitempty"`
	SaveError string `json:"save_error,omitempty"`
}

type rewriteFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type multiApplyOut struct {
	Pattern         string              `json:"pattern"`
	Failed          []rewriteFailure    `json:"failed,omitempty"`
	DryRun          bool                `json:"dry_run,omitempty"`
	FileCount       int                 `json:"file_count"`
	TotalMatches    int                 `json:"total_matches"`
	Changed         []multiApplyFileOut `json:"changed,omitempty"`
	Blocked         []rewriteBlocked    `json:"blocked,omitempty"`
	UnobservedFiles int                 `json:"unobserved_files,omitempty"`
	Truncated       bool                `json:"truncated,omitempty"`
	Note            string              `json:"note,omitempty"`
}

func parseRewritePaths(args map[string]any) (paths []string, ok bool) {
	raw, ok := args["paths"]
	if !ok {
		return nil, false
	}
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					paths = append(paths, s)
				}
			}
		}
	case []string:
		for _, s := range v {
			s = strings.TrimSpace(s)
			if s != "" {
				paths = append(paths, s)
			}
		}
	}
	return paths, true
}

func (t *CodeRewriteTool) buildWalkRequest(ctx context.Context, tctx tools.ToolContext, paths []string, pattern, lang string, recursive bool) (structrewrite.WalkRequest, []rewriteBlocked, error) {
	if strings.TrimSpace(lang) != "" {
		if _, ok := structrewrite.SupportedLanguage(lang, ""); !ok {
			anchor := "."
			if len(paths) > 0 {
				anchor = paths[0]
			}
			return structrewrite.WalkRequest{}, nil, sourceview.LanguageUnknown(anchor, lang)
		}
	}
	// Resolve every subpath before the active-root walk.
	activePath := tctx.ActiveRootPath()
	walkable := make([]string, 0, len(paths))
	var blocked []rewriteBlocked
	for _, p := range paths {
		resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, p)
		if err != nil {
			return structrewrite.WalkRequest{}, nil, err
		}
		if resolved.Root.Path != activePath {
			blocked = append(blocked, rewriteBlocked{Path: resolved.DisplayPath, Code: "PATH_OUTSIDE_SESSION_ROOT"})
			continue
		}
		walkable = append(walkable, resolved.ScopeRel)
	}
	pathIncluded, err := t.Boundary.CompileReadFilter(ctx, tctx.ActiveRootPath(), tctx.ProfileID())
	if err != nil {
		return structrewrite.WalkRequest{}, nil, err
	}
	return structrewrite.WalkRequest{
		Root:         activePath,
		Subpaths:     walkable,
		Recursive:    recursive,
		Pattern:      pattern,
		LangName:     lang,
		PathIncluded: pathIncluded,
	}, blocked, nil
}

func (t *CodeRewriteTool) runMultiApply(ctx context.Context, args map[string]any, tctx tools.ToolContext, paths []string, pattern, rewrite, lang string, dryRun bool) (string, error) {
	return retryEditorDocument(ctx, strings.Join(paths, ", "), func() (string, error) {
		return t.prepareMultiApply(ctx, args, tctx, paths, pattern, rewrite, lang, dryRun)
	})
}

func (t *CodeRewriteTool) prepareMultiApply(ctx context.Context, args map[string]any, tctx tools.ToolContext, paths []string, pattern, rewrite, lang string, dryRun bool) (string, error) {
	recursive, _ := args["recursive"].(bool)
	wreq, blockedSubpaths, err := t.buildWalkRequest(ctx, tctx, paths, pattern, lang, recursive)
	if err != nil {
		return "", err
	}
	out := multiApplyOut{
		Pattern: wreq.Pattern,
		DryRun:  dryRun,
		Blocked: blockedSubpaths,
	}
	if len(wreq.Subpaths) == 0 && len(paths) > 0 {
		// Every requested path was blocked; an empty Subpaths walk would sweep
		// the whole root instead.
		out.FileCount = len(out.Blocked)
		out.Note = "No walkable paths in the session folder. Paths in other folders are listed as blocked — rewrite them one at a time with code_rewrite(path=\"@<label>/…\")."
		publishRewriteBlocked(tctx, out.Blocked)
		b, err := surveyjson.Marshal(out)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	var (
		totalMatches int
		patternErr   error
		parseBlocked string
		parseError   string
		parseCode    string
		parseData    map[string]any
	)
	var plans []rewritePlan
	stats, walkErr := repomap.WalkSourceFiles(ctx, repomap.SourceWalkOptions{
		Root:         wreq.Root,
		Subpaths:     wreq.Subpaths,
		Recursive:    wreq.Recursive,
		MaxFiles:     repomap.DefaultWalkMaxFiles,
		MaxFileBytes: repomap.DefaultWalkMaxFileBytes,
		PathIncluded: wreq.PathIncluded,
	}, func(relSlash, absPath string) error {
		if patternErr != nil {
			return repomap.ErrWalkStop
		}
		fr := t.prepareRewriteFile(ctx, tctx, wreq, relSlash, absPath, rewrite, dryRun)
		if fr.abortErr != nil {
			return fr.abortErr
		}
		if fr.patternErr != nil {
			patternErr = fr.patternErr
			return repomap.ErrWalkStop
		}
		if fr.blockCode != "" {
			out.Blocked = append(out.Blocked, rewriteBlocked{Path: relSlash, Code: fr.blockCode, Details: fr.rejectData})
			if rewriteBlockedBySyntax(fr.blockCode) && parseBlocked == "" {
				parseBlocked = relSlash
				parseError = fr.parseError
				parseCode = fr.blockCode
				parseData = fr.rejectData
			}
			return nil
		}
		if fr.matches > 0 {
			totalMatches += fr.matches
		}
		if fr.changed {
			out.Changed = append(out.Changed, multiApplyFileOut{
				Path: relSlash, Language: changedLang(wreq, relSlash), Matches: fr.matches, Diff: fr.diff,
				ParseError:  fr.parseError,
				SyntaxIssue: fr.syntaxIssue,
			})
		}
		if fr.plan != nil {
			plans = append(plans, *fr.plan)
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, repomap.ErrWalkStop) {
		return "", walkErr
	}
	if patternErr != nil {
		anchor := "."
		if len(wreq.Subpaths) > 0 {
			anchor = wreq.Subpaths[0]
		}
		return "", sourceview.PatternInvalid(anchor, patternErr)
	}
	out.TotalMatches = totalMatches
	out.FileCount = len(out.Changed) + len(out.Blocked)
	out.UnobservedFiles = stats.UnobservedFiles
	out.Truncated = stats.FilesCapHit || stats.UnobservedFiles > 0
	if len(out.Changed) == 0 && len(out.Blocked) == 0 && totalMatches == 0 && !out.Truncated {
		out.Note = multiUnsupportedNote(wreq.Subpaths)
	}
	if !dryRun && parseBlocked != "" {
		if parseData == nil {
			parseData = map[string]any{"tool": "code_rewrite", "path": parseBlocked, "parse_error": parseError}
		}
		return "", &toolrejection.ToolReject{Code: parseCode, Data: parseData}
	}
	if !dryRun {
		if err := landRewritePlans(ctx, tctx, plans, &out); err != nil {
			return "", err
		}
	}
	publishRewriteBlocked(tctx, out.Blocked)
	b, err := surveyjson.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func changedLang(wreq structrewrite.WalkRequest, relSlash string) string {
	if name, ok := structrewrite.SupportedLanguage(wreq.LangName, relSlash); ok {
		return name
	}
	return ""
}

// fileApplyResult retains a preflight result without changing its source.
type fileApplyResult struct {
	syntaxIssue *rewriteBlocked
	changed     bool
	matches     int
	diff        string
	blockCode   string
	patternErr  error
	abortErr    error
	parseError  string
	rejectData  map[string]any
	plan        *rewritePlan
}

func (t *CodeRewriteTool) prepareRewriteFile(ctx context.Context, tctx tools.ToolContext, wreq structrewrite.WalkRequest, relSlash, absPath, rewrite string, dryRun bool) (fr fileApplyResult) {
	defer func() {
		if r := recover(); r != nil {
			// Preserve the recovered cause in the per-file failure.
			fr = fileApplyResult{blockCode: "STRUCTURAL_APPLY_FAILED", rejectData: map[string]any{"reason": fmt.Sprintf("source rewrite panicked: %v", r), "path": relSlash}}
		}
	}()
	if _, ok := structrewrite.SupportedLanguage(wreq.LangName, relSlash); !ok {
		return fileApplyResult{}
	}
	if !dryRun {
		if err := assertProfileWriteScope(ctx, t.Boundary, tctx, relSlash, "code_rewrite"); err != nil {
			if isWriteScopeDenied(err) {
				return fileApplyResult{blockCode: "WRITE_SCOPE_DENIED", rejectData: map[string]any{"path": relSlash, "profile": tctx.ProfileID(), "patterns_list": formatGlobsMarkdown(t.Boundary.WriteGlobsForProfile(ctx, tctx.ProfileID()))}}
			}
			return fileApplyResult{abortErr: err}
		}
	}
	resolved := walkFileResolved(tctx, wreq.Root, relSlash, absPath)
	return t.planRewriteFile(ctx, tctx, wreq, resolved, rewrite, dryRun)
}

// walkFileResolved addresses one walked file the way path resolution would,
// so the open-document lookup and the write door see the same identity.
func walkFileResolved(tctx tools.ToolContext, walkRoot, relSlash, absPath string) projectpaths.Resolved {
	root := projectroot.RootRef{Path: walkRoot}
	for _, candidate := range tctx.Roots {
		if filepath.Clean(candidate.Path) == filepath.Clean(walkRoot) {
			root = candidate
			break
		}
	}
	return projectpaths.Resolved{Abs: absPath, DisplayPath: relSlash, ScopeRel: relSlash, Root: root}
}

func (t *CodeRewriteTool) planRewriteFile(ctx context.Context, tctx tools.ToolContext, wreq structrewrite.WalkRequest, resolved projectpaths.Resolved, rewrite string, dryRun bool) fileApplyResult {
	relSlash := resolved.DisplayPath
	st, err := loadRewriteSource(ctx, tctx, resolved, dryRun)
	if err != nil {
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) {
			return fileApplyResult{blockCode: reject.Code, rejectData: reject.Data}
		}
		return fileApplyResult{blockCode: "STRUCTURAL_APPLY_FAILED", rejectData: map[string]any{"path": relSlash, "reason": err.Error()}}
	}
	res, err := structrewrite.Run(ctx, structrewrite.Request{
		LangName: wreq.LangName,
		Filename: relSlash,
		Source:   []byte(st.Content),
		Pattern:  wreq.Pattern,
		Fix:      rewrite,
	})
	if err != nil {
		if reject := sourceview.ParseReject(relSlash, "source", err); reject != nil {
			return fileApplyResult{blockCode: reject.Code, rejectData: reject.Data}
		}
		var invalid *structrewrite.PatternError
		if errors.As(err, &invalid) {
			return fileApplyResult{patternErr: err}
		}
		return fileApplyResult{blockCode: "STRUCTURAL_APPLY_FAILED", rejectData: map[string]any{"path": relSlash, "reason": err.Error()}}
	}
	if len(res.Matches) == 0 || !res.Changed {
		return fileApplyResult{matches: len(res.Matches)}
	}
	finalContent := string(res.Rewritten)
	if blocked, ok := contentBlocked(relSlash, len(res.Matches), finalContent); ok {
		return blocked
	}
	if dryRun {
		fr := fileApplyResult{
			changed: true,
			matches: len(res.Matches),
			diff:    sourceview.UnifiedDiff(relSlash, relSlash, sourceview.SplitLines(st.Content), sourceview.SplitLines(finalContent), 3),
		}
		before := st.Content
		if healthErr := rejectIfSyntaxUnhealthy(ctx, "code_rewrite", relSlash, &before, finalContent, mutationSeam{}); healthErr != nil {
			var reject *toolrejection.ToolReject
			if errors.As(healthErr, &reject) {
				fr.syntaxIssue = &rewriteBlocked{Path: relSlash, Code: reject.Code, Details: reject.Data}
				fr.parseError, _ = reject.Data["parse_error"].(string)
				if fr.parseError == "" {
					fr.parseError, _ = reject.Data["parse_failure"].(string)
					if fr.parseError == "" {
						fr.parseError = fmt.Sprint(reject.Data["parse_status"])
					}
				}
			}
		}
		return fr
	}
	if err := beforeWorkerMutation(ctx, tctx, relSlash); err != nil {
		return fileApplyResult{abortErr: err}
	}
	reportTextIntent(tctx, resolved, st, true, finalContent)
	if t.ContentApply != nil {
		before := st.Content
		proposed := finalContent
		var gateErr error
		finalContent, gateErr = t.ContentApply.GateApply(ctx, "code_rewrite", relSlash, &before, finalContent, tctx)
		if gateErr != nil {
			var reject *toolrejection.ToolReject
			if errors.As(gateErr, &reject) {
				return fileApplyResult{matches: len(res.Matches), blockCode: reject.Code, rejectData: reject.Data}
			}
			return fileApplyResult{abortErr: gateErr}
		}
		if finalContent != proposed {
			// Editor plans land in one transaction without landEditedText.
			if blocked, ok := contentBlocked(relSlash, len(res.Matches), finalContent); ok {
				return blocked
			}
			reportTextIntent(tctx, resolved, st, true, finalContent)
		}
	}
	before := st.Content
	if healthErr := rejectIfSyntaxUnhealthy(ctx, "code_rewrite", relSlash, &before, finalContent, mutationSeam{}); healthErr != nil {
		var reject *toolrejection.ToolReject
		if errors.As(healthErr, &reject) {
			parseError, _ := reject.Data["parse_error"].(string)
			return fileApplyResult{
				matches: len(res.Matches), blockCode: reject.Code, parseError: parseError, rejectData: reject.Data,
			}
		}
		return fileApplyResult{abortErr: healthErr}
	}
	return fileApplyResult{changed: true, matches: len(res.Matches), plan: &rewritePlan{
		resolved: resolved, source: st, content: finalContent,
	}}
}

// contentBlocked records a file whose planned text the gateway cannot retain.
func contentBlocked(relSlash string, matches int, content string) (fileApplyResult, bool) {
	var reject *toolrejection.ToolReject
	if !errors.As(guardMutationContent("code_rewrite", relSlash, content), &reject) {
		return fileApplyResult{}, false
	}
	return fileApplyResult{matches: matches, blockCode: reject.Code, rejectData: reject.Data}, true
}

func isWriteScopeDenied(err error) bool {
	var scopeErr *sandbox.ScopeError
	if errors.As(err, &scopeErr) && scopeErr.Kind == sandbox.ScopeWrite {
		return true
	}
	code, ok := toolRejectCode(err)
	return ok && code == "WRITE_SCOPE_DENIED"
}

func toolRejectCode(err error) (string, bool) {
	var reject *toolrejection.ToolReject
	if errors.As(err, &reject) {
		return reject.Code, true
	}
	return "", false
}

func multiUnsupportedNote(paths []string) string {
	scope := "focus paths"
	if len(paths) == 0 {
		scope = "project"
	}
	return fmt.Sprintf("no tree-sitter matches under %s; unsupported extensions are skipped — narrow paths or use edit", scope)
}

func publishRewriteBlocked(tctx tools.ToolContext, blocked []rewriteBlocked) {
	if tctx.Out == nil {
		return
	}
	for _, item := range blocked {
		details := make(map[string]any, len(item.Details)+1)
		for key, value := range item.Details {
			details[key] = value
		}
		details["path"] = item.Path
		tctx.Out.Facts = tctx.Out.Facts.WithFeedback(item.Code, details, &api.FeedbackSubject{Kind: "path", ID: item.Path})
	}
}

func rewriteBlockedBySyntax(code string) bool {
	switch code {
	case "MUTATION_BROKE_PARSE", "MUTATION_REPAIR_NOT_IMPROVED", "MUTATION_PARSE_INCOMPLETE", "MUTATION_PARSE_FAILED", "SOURCE_PARSE_INCOMPLETE", "SOURCE_PARSE_FAILED":
		return true
	default:
		return false
	}
}
