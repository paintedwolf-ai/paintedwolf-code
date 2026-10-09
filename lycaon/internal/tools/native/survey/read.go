package survey

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/docrefs"
	"github.com/lycaon/lycaon/internal/tools/fileage"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReadTool reads file contents under the attached project roots.
type ReadTool struct {
	Boundary   *sandbox.Boundary
	Escalation *ReadEscalationStore
	// Age attaches file-age context to read receipts; nil disables it.
	Age *fileage.Provider
	// Ledger lets read receipts report which named references this session read.
	Ledger  guidance.EvidenceLedgerReader
	Catalog *sourcecatalog.Catalog
}

// attachReadReceipt wraps read output with its survey receipt, file-age context
// for a tracked file, and the ledger identity of the served bytes.
func (t *ReadTool) attachReadReceipt(ctx context.Context, tctx tools.ToolContext, path string, bytesReturned int, truncated bool, output string, source *surveyreceipt.SourceContext) string {
	r := surveyreceipt.New("read", path, 1, bytesReturned, truncated)
	r.Source = source
	self, selfOK := t.Age.Position(ctx, tctx.ActiveRootPath(), path)
	if selfOK {
		r.Age = &surveyreceipt.AgeContext{
			LastChanged:  self.LastChanged.Format("2006-01-02"),
			OlderThanPct: self.OlderThanPct,
			TrackedFiles: self.TrackedFiles,
			Summary:      self.Summary(),
		}
	}
	r.References = t.referencesFor(ctx, tctx, path, self, selfOK, output)
	return surveyreceipt.Attach(output, r)
}

// maxNamedRefs bounds how many named references a receipt carries.
const maxNamedRefs = 12

// referencesFor reports in-repo files the content names that exist in read
// scope, with their history and session read state relative to the read file.
// Nil when none resolve. selfPos is the read file's own age position.
func (t *ReadTool) referencesFor(ctx context.Context, tctx tools.ToolContext, self string, selfPos fileage.Position, selfOK bool, content string) *surveyreceipt.RefContext {
	candidates := docrefs.Extract(content)
	if len(candidates) == 0 {
		return nil
	}
	root := tctx.ActiveRootPath()
	sessionLedger, haveSessionLedger := t.sessionLedger(ctx, tctx)
	seen := make(map[string]bool)
	named := make([]surveyreceipt.Ref, 0, len(candidates))
	for _, cand := range candidates {
		resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, cand)
		if err != nil {
			continue
		}
		disp := resolved.DisplayPath
		if disp == self || seen[disp] {
			continue
		}
		isRegular := false
		if t.Catalog != nil {
			current := catalogOrProcess(t.Catalog).Current(ctx, tctx.ProjectID, []sourcecatalog.Root{{ID: resolved.Root.ID, Path: resolved.Root.Path}})
			if current.State == sourcecatalog.StateReady {
				rel := projectroot.ScopeRel(resolved.Root, resolved.Abs)
				if entry, ok := current.Entry(resolved.Root.ID, rel); ok {
					if entry.IsDir {
						continue
					}
					isRegular = true
				}
			}
		}
		if !isRegular {
			if info, err := os.Stat(resolved.Abs); err != nil || info.IsDir() {
				continue
			}
		}
		seen[disp] = true
		ref := surveyreceipt.Ref{Path: disp}
		if pos, ok := t.Age.Position(ctx, root, disp); ok {
			ref.LastChanged = pos.LastChanged.Format("2006-01-02")
			ref.Newer = selfOK && pos.LastChanged.After(selfPos.LastChanged)
		}
		if haveSessionLedger {
			read := evidence.PathObserved(sessionLedger, disp)
			ref.Read = &read
		}
		named = append(named, ref)
	}
	if len(named) == 0 {
		return nil
	}
	truncated := false
	if len(named) > maxNamedRefs {
		named = named[:maxNamedRefs]
		truncated = true
	}
	newer := 0
	unread := 0
	for _, ref := range named {
		if ref.Newer {
			newer++
		}
		if ref.Read != nil && !*ref.Read {
			unread++
		}
	}
	return &surveyreceipt.RefContext{
		Named:       named,
		NewerCount:  newer,
		UnreadCount: unread,
		Truncated:   truncated,
		Summary:     refSummary(len(named), newer, unread, selfOK),
	}
}

func (t *ReadTool) sessionLedger(ctx context.Context, tctx tools.ToolContext) (evidence.Ledger, bool) {
	if t == nil || t.Ledger == nil {
		return evidence.Ledger{}, false
	}
	sessionID := strings.TrimSpace(tctx.SessionID)
	if sessionID == "" {
		return evidence.Ledger{}, false
	}
	ledger, err := t.Ledger.LoadLedger(ctx, sessionID)
	if err != nil {
		return evidence.Ledger{}, false
	}
	return ledger, true
}

// refSummary counts named files, those changed more recently when comparable,
// and those not yet read this session.
func refSummary(count, newer, unread int, haveAgeBaseline bool) string {
	noun := "files"
	if count == 1 {
		noun = "file"
	}
	base := fmt.Sprintf("names %d in-repo %s it references", count, noun)
	var parts []string
	if haveAgeBaseline && newer > 0 {
		parts = append(parts, fmt.Sprintf("%d changed more recently than this file", newer))
	}
	if unread > 0 {
		unreadNoun := "files"
		if unread == 1 {
			unreadNoun = "file"
		}
		parts = append(parts, fmt.Sprintf("%d %s not read this session", unread, unreadNoun))
	}
	if len(parts) == 0 {
		return base
	}
	return base + "; " + strings.Join(parts, "; ")
}

func (t *ReadTool) Name() string { return "read" }

type readLineLimitResult struct {
	limit     int
	clamped   bool
	requested *int
}

func readOffset(args map[string]any) int {
	return toolkit.BoundedIntArg(args, "offset", 1, 1, 1<<30).Effective
}

func readLineLimit(args map[string]any) readLineLimitResult {
	limit := readcaps.LineLimit
	var requested *int
	if raw, ok := args["limit"]; ok && raw != nil {
		arg := toolkit.BoundedIntArg(args, "limit", limit, 1, 1<<30)
		if arg.Requested != nil && arg.Effective > 0 {
			requested = arg.Requested
			limit = arg.Effective
			if limit > readcaps.LineLimit {
				limit = readcaps.LineLimit
			}
		}
	}
	clamped := requested != nil && *requested != limit
	return readLineLimitResult{limit: limit, clamped: clamped, requested: requested}
}

func (t *ReadTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (out string, err error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return "", toolkit.MissingArg("path")
	}
	resolved, err := projectpaths.ResolveRead(ctx, t.Boundary, tctx, path)
	if err != nil {
		return "", err
	}
	fullPath := resolved.Abs
	path = resolved.DisplayPath
	target, isTarget := sourceview.ReportTarget(tctx, resolved, api.AgentActivityKindReading)
	st, err := sourceview.LoadText(ctx, "read", sourceview.AccessRead, tctx, resolved)
	if os.IsNotExist(err) {
		// The result carries the spill's own path, so the next read names it directly.
		if spill, ok := projectpaths.ResolveMisplacedSpill(tctx, path); ok {
			resolved, fullPath, path = spill, spill.Abs, spill.DisplayPath
			target, isTarget = sourceview.ReportTarget(tctx, resolved, api.AgentActivityKindReading)
			st, err = sourceview.LoadText(ctx, "read", sourceview.AccessRead, tctx, resolved)
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			return "", sourceview.PathNotFound("read", path, fullPath)
		}
		return "", fmt.Errorf("read failed: %w", err)
	}
	// A credential file's text reaches no one until its exposure is recorded.
	if err := tctx.ObserveCredentialRead(ctx, fullPath, path, st.Content); err != nil {
		return "", fmt.Errorf("record credential file read: %w", err)
	}
	capture := &readCapture{}
	defer func() {
		if err != nil {
			return
		}
		if st.Editor != nil {
			if documents, ok := sourceview.DocumentsFor(tctx, resolved); ok {
				documents.RememberAgentRead(tctx.ProjectID, tctx.SessionID, *st.Editor)
			}
		}
		if isTarget {
			capture.record(tctx, target, st.Editor)
		}
	}()
	text := st.Content
	totalLines := toolkit.CountLines(text)
	source := sourceview.Stamp(ctx, tctx, fullPath, st)
	if readSymbolArg(args) != "" {
		return t.runSymbol(ctx, path, text, args, source, capture)
	}
	if readHasRanges(args) {
		return t.runBatch(path, text, args, source, capture)
	}
	offset := readOffset(args)
	if totalLines == 0 {
		if offset > 1 {
			return "", readOffsetBeyondEOF(path, offset, totalLines)
		}
	} else if offset > totalLines {
		return "", readOffsetBeyondEOF(path, offset, totalLines)
	}
	mode := readModeArg(args)
	if readWantsOutline(mode, args, totalLines) {
		strike := 1
		if t.Escalation != nil {
			strike = t.Escalation.BumpUnboundedRead(tctx.SessionID, path)
		}
		if strike >= 2 {
			capture.whole = true
			resp := buildReadLiteralFullResponse(path, text)
			raw, err := surveyjson.Marshal(resp)
			if err != nil {
				return "", fmt.Errorf("read encode: %w", err)
			}
			return t.attachReadReceipt(ctx, tctx, path, len(raw), false, string(raw), source), nil
		}
		outline, err := fileoutline.BuildContent(ctx, path, []byte(text), int64(len(text)))
		if err != nil {
			return "", fmt.Errorf("read outline failed: %w", err)
		}
		if outline.ParseFailure != nil {
			sourceview.ReportParseFailures(tctx, []tsparse.FileFailure{{Path: path, Phase: "source", Failure: outline.ParseFailure}}, 1)
		}
		resp := buildReadZoomedOutlineResponse(ctx, path, text, outline)
		raw, err := surveyjson.Marshal(resp)
		if err != nil {
			return "", fmt.Errorf("read encode: %w", err)
		}
		out := string(raw)
		if resp.Total > 0 {
			out, err = toolkit.PatchCoverage(out, resp.Selected, resp.Total)
			if err != nil {
				return "", fmt.Errorf("read encode: %w", err)
			}
		}
		return t.attachReadReceipt(ctx, tctx, path, len(out), false, out, source), nil
	}

	limitRes := readLineLimit(args)
	page, _, endLine, hasMore := PaginateLines(text, offset, limitRes.limit)
	if offset == 1 && !hasMore {
		capture.whole = true
	} else {
		capture.lines(offset, endLine)
	}

	resp := ReadResponse{
		Path:       path,
		Mode:       "content",
		Content:    hostmarker.FormatNumberedLines(page, offset),
		TotalLines: totalLines,
		Offset:     offset,
		Limit:      limitRes.limit,
		EndLine:    endLine,
		Truncated:  hasMore,
	}
	if hasMore {
		next := endLine + 1
		resp.NextOffset = &next
	}
	var bannerParts []string
	if limitRes.clamped && limitRes.requested != nil {
		bannerParts = append(bannerParts,
			fmt.Sprintf("limit capped at %d lines (requested %d)", limitRes.limit, *limitRes.requested),
		)
	}
	if hasMore {
		remaining := totalLines - endLine
		bannerParts = append(bannerParts,
			fmt.Sprintf("%d more lines; use offset=%d limit=%d", remaining, endLine+1, limitRes.limit),
		)
	}
	if len(bannerParts) > 0 {
		resp.TruncationBanner = toolkit.TruncationBanner(strings.Join(bannerParts, "; "))
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("read encode: %w", err)
	}
	return t.attachReadReceipt(ctx, tctx, path, len(raw), resp.Truncated, string(raw), source), nil
}

func readOffsetBeyondEOF(path string, offset, totalLines int) error {
	return &toolrejection.ToolReject{
		Code: "READ_OFFSET_BEYOND_EOF",
		Data: map[string]any{
			"path":            path,
			"offset":          offset,
			"total_lines":     totalLines,
			"read_empty_file": totalLines == 0,
		},
	}
}
