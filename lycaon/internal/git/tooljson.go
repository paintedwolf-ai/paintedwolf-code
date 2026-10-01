package git

import (
	"errors"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/paginate"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// ToolFailure distinguishes host rejections from command failures.
type ToolFailure struct {
	Available bool   `json:"available"`
	Error     string `json:"error"`
	Code      string `json:"code,omitempty"`
}

// MarshalToolFailure preserves diagnostics and the typed failure together.
// A serialized error is still a failed invocation, never a successful result.
func MarshalToolFailure(err error) (string, error) {
	payload := ToolFailure{
		Available: false,
		Error:     NormalizeToolError(err),
		Code:      ToolErrorCode(err),
	}
	raw, marshalErr := surveyjson.Marshal(payload)
	if marshalErr != nil {
		return "", errors.Join(err, marshalErr)
	}
	return string(raw), err
}

// ToolErrorCode returns a host rejection code or an empty string for command failures.
func ToolErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var unavailable *gitengine.UnavailableError
	if errors.As(err, &unavailable) {
		return "GIT_ENGINE_UNAVAILABLE"
	}
	var unsafeCfg *gitexec.UnsafeRepoConfigError
	if errors.As(err, &unsafeCfg) {
		return unsafeCfg.Code()
	}
	var signing *gitexec.SigningUnsupportedError
	if errors.As(err, &signing) {
		return signing.Code()
	}
	return ""
}

// NormalizeToolError maps git CLI failures to a single-line agent-readable message.
func NormalizeToolError(err error) string {
	if err == nil {
		return "git command failed"
	}
	var cf *CommitFailed
	if errors.As(err, &cf) {
		return cf.Summary()
	}
	var unavailable *gitengine.UnavailableError
	if errors.As(err, &unavailable) {
		return "the app's bundled git engine is unavailable — reinstall or repair the app"
	}
	var unsafeCfg *gitexec.UnsafeRepoConfigError
	if errors.As(err, &unsafeCfg) {
		return "this repository's config declares commands the app will not run (" +
			unsafeCfg.KeyList() + ") — use git in your terminal for this repository"
	}
	msg := strings.TrimSpace(err.Error())
	if errors.Is(err, ErrNotRepository) {
		return "not a git repository"
	}
	for _, prefix := range []string{
		"git status failed: ",
		"git diff failed: ",
		"git diff --numstat failed: ",
		"git log failed: ",
		"git show failed: ",
		"git blame failed: ",
		"git restore failed: ",
		"git rev-parse ",
		"git for-each-ref failed: ",
	} {
		if strings.HasPrefix(msg, prefix) {
			msg = strings.TrimSpace(strings.TrimPrefix(msg, prefix))
			break
		}
	}
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		msg = strings.TrimSpace(msg[:idx])
	}
	return msg
}

// StatusToolResponse combines whole-tree branch facts with counts and files for the selected paths.
type StatusToolResponse struct {
	Summary         bool              `json:"summary,omitempty"`
	Available       bool              `json:"available"`
	Branch          string            `json:"branch,omitempty"`
	HeadShort       string            `json:"head_short,omitempty"`
	Upstream        string            `json:"upstream,omitempty"`
	Ahead           int               `json:"ahead"`
	Behind          int               `json:"behind"`
	Dirty           bool              `json:"dirty"`
	StagedCount     int               `json:"staged_count"`
	UnstagedCount   int               `json:"unstaged_count"`
	UntrackedCount  int               `json:"untracked_count"`
	Paths           []string          `json:"paths,omitempty"`
	Offset          int               `json:"offset"`
	Limit           int               `json:"limit"`
	Files           []GitStatusEntry  `json:"files"`
	FilesTotal      int               `json:"files_total"`
	FilesTruncated  bool              `json:"files_truncated"`
	NextOffset      *int              `json:"next_offset,omitempty"`
	GroupDepth      int               `json:"group_depth,omitempty"`
	Groups          []StatusToolGroup `json:"groups,omitempty"`
	GroupsTotal     int               `json:"groups_total,omitempty"`
	GroupsTruncated bool              `json:"groups_truncated,omitempty"`
	RecentCommits   []string          `json:"recent_commits,omitempty"`
	Ignored         bool              `json:"ignored,omitempty"`
	Error           string            `json:"error,omitempty"`
}

// StatusToolGroup rolls the selection up by leading path segments: which
// areas of the tree changed, and how much, before any file is listed.
type StatusToolGroup struct {
	Prefix    string `json:"prefix"`
	Files     int    `json:"files"`
	Staged    int    `json:"staged"`
	Unstaged  int    `json:"unstaged"`
	Untracked int    `json:"untracked"`
}

// StatusToolPage selects path prefixes and pages files or depth-based groups. Nonpositive limits are unbounded.
type StatusToolPage struct {
	Summary    bool
	Paths      []string
	Offset     int
	Limit      int
	GroupDepth int
	Ignored    bool
}

// BuildStatusToolResponse selects the exact file page returned to the caller.
func BuildStatusToolResponse(status *GitStatus, page StatusToolPage) StatusToolResponse {
	if status == nil {
		return StatusToolResponse{Files: []GitStatusEntry{}}
	}

	selected := selectStatusFiles(status.Files, page.Paths)
	resp := StatusToolResponse{
		Available:     true,
		Summary:       page.Summary,
		Branch:        status.Branch,
		HeadShort:     status.HeadShort,
		Upstream:      status.Upstream,
		Ahead:         status.Ahead,
		Behind:        status.Behind,
		Dirty:         status.Dirty,
		Paths:         page.Paths,
		Offset:        max(page.Offset, 0),
		Limit:         max(page.Limit, 0),
		RecentCommits: status.RecentCommits,
	}
	resp.StagedCount, resp.UnstagedCount, resp.UntrackedCount = countStatusFiles(selected)
	resp.Files, resp.FilesTotal, resp.FilesTruncated, resp.NextOffset = paginate.Slice(selected, resp.Offset, resp.Limit)
	if resp.Files == nil {
		resp.Files = []GitStatusEntry{}
	}
	if page.Summary {
		resp.Files = []GitStatusEntry{}
		resp.FilesTruncated = false
		resp.NextOffset = nil
		if page.GroupDepth == 0 {
			page.GroupDepth = 1
		}
	}
	if page.GroupDepth > 0 {
		resp.GroupDepth = page.GroupDepth
		groups := groupStatusFiles(selected, page.GroupDepth)
		resp.Groups, resp.GroupsTotal, resp.GroupsTruncated, _ = paginate.Slice(groups, 0, resp.Limit)
	}
	return resp
}

func MarshalStatusToolResponse(resp StatusToolResponse, err error) (string, error) {
	if err != nil {
		return MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// groupStatusFiles tallies entries by their first depth path segments, largest
// groups first and ties by prefix so the order is stable across calls.
func groupStatusFiles(files []GitStatusEntry, depth int) []StatusToolGroup {
	index := map[string]int{}
	var groups []StatusToolGroup
	for _, f := range files {
		prefix := pathPrefix(f.Path, depth)
		i, ok := index[prefix]
		if !ok {
			i = len(groups)
			index[prefix] = i
			groups = append(groups, StatusToolGroup{Prefix: prefix})
		}
		staged, unstaged, untracked := countStatusFiles([]GitStatusEntry{f})
		groups[i].Files++
		groups[i].Staged += staged
		groups[i].Unstaged += unstaged
		groups[i].Untracked += untracked
	}
	sort.SliceStable(groups, func(a, b int) bool {
		if groups[a].Files != groups[b].Files {
			return groups[a].Files > groups[b].Files
		}
		return groups[a].Prefix < groups[b].Prefix
	})
	return groups
}

// pathPrefix keeps the first depth segments of a slash path; a shorter path is
// its own prefix.
func pathPrefix(path string, depth int) string {
	rest := path
	end := 0
	for i := 0; i < depth; i++ {
		idx := strings.IndexByte(rest, '/')
		if idx < 0 {
			return path
		}
		end += idx + 1
		rest = rest[idx+1:]
	}
	return path[:end-1]
}

// selectStatusFiles keeps the entries under any of the prefixes, in status order.
func selectStatusFiles(files []GitStatusEntry, prefixes []string) []GitStatusEntry {
	cleaned := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" || p == "." {
			return files
		}
		cleaned = append(cleaned, p)
	}
	if len(cleaned) == 0 {
		return files
	}
	out := make([]GitStatusEntry, 0, len(files))
	for _, f := range files {
		for _, p := range cleaned {
			if f.Path == p || strings.HasPrefix(f.Path, p+"/") {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// countStatusFiles counts untracked entries as unstaged alongside porcelain XY changes.
func countStatusFiles(files []GitStatusEntry) (staged, unstaged, untracked int) {
	for _, f := range files {
		if len(f.Status) != 2 {
			continue
		}
		if f.Status == "??" {
			untracked++
		}
		if f.Status[0] != ' ' && f.Status[0] != '?' {
			staged++
		}
		if f.Status[1] != ' ' && f.Status[1] != '!' {
			unstaged++
		}
	}
	return staged, unstaged, untracked
}

// DiffToolEntry carries one file; DiffTruncated marks hunks cut at a line boundary.
type DiffToolEntry struct {
	DiffSpillPath string `json:"diff_spill_path,omitempty"`
	DiffLines     int    `json:"diff_lines,omitempty"`
	Path          string `json:"path"`
	Insertions    int    `json:"insertions"`
	Deletions     int    `json:"deletions"`
	Untracked     bool   `json:"untracked,omitempty"`
	Diff          string `json:"diff,omitempty"`
	DiffBytes     int    `json:"diff_bytes,omitempty"`
	DiffTruncated bool   `json:"diff_truncated,omitempty"`
}

// DiffToolResponse carries one bounded file page and echoes the comparison inputs.
type DiffToolResponse struct {
	WireSpillPath  string          `json:"wire_spill_path,omitempty"`
	Available      bool            `json:"available"`
	Stat           bool            `json:"stat,omitempty"`
	Staged         bool            `json:"staged,omitempty"`
	BaseRef        string          `json:"base_ref,omitempty"`
	HeadRef        string          `json:"head_ref,omitempty"`
	BaseOID        string          `json:"base_oid,omitempty"`
	HeadOID        string          `json:"head_oid,omitempty"`
	Paths          []string        `json:"paths,omitempty"`
	Untracked      bool            `json:"untracked,omitempty"`
	Offset         int             `json:"offset"`
	Limit          int             `json:"limit"`
	MaxBytes       int             `json:"max_bytes,omitempty"`
	Files          []DiffToolEntry `json:"files"`
	FilesTotal     int             `json:"files_total"`
	FilesTruncated bool            `json:"files_truncated"`
	NextOffset     *int            `json:"next_offset,omitempty"`
	Error          string          `json:"error,omitempty"`
}

// DiffToolPage bounds files and hunk bytes; nonpositive limits are unbounded.
type DiffToolPage struct {
	Offset   int
	Limit    int
	MaxBytes int
}

// MarshalDiffToolResponse encodes a git_diff page or structured failure.
func MarshalDiffToolResponse(resp DiffToolResponse, err error) (string, error) {
	if err != nil {
		return MarshalToolFailure(err)
	}
	resp.Available = true
	if resp.Files == nil {
		resp.Files = []DiffToolEntry{}
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// SplitDiffBlocks uses file headers; hunk lines carry a distinct leading marker.
func SplitDiffBlocks(text string) []string {
	const header = "diff --git "
	var blocks []string
	start := -1
	for pos := 0; pos < len(text); {
		end := strings.IndexByte(text[pos:], '\n')
		lineEnd := len(text)
		if end >= 0 {
			lineEnd = pos + end + 1
		}
		if strings.HasPrefix(text[pos:], header) {
			if start >= 0 {
				blocks = append(blocks, text[start:pos])
			}
			start = pos
		}
		pos = lineEnd
	}
	if start >= 0 {
		blocks = append(blocks, text[start:])
	}
	return blocks
}

// CountHunkLines tallies added and removed lines in one file's hunks; the
// `+++`/`---` file headers are not counted.
func CountHunkLines(diff string) (insertions, deletions int) {
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			insertions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return insertions, deletions
}

// FitDiffPage keeps whole entries and clips an oversized first entry at a line boundary.
func FitDiffPage(entries []DiffToolEntry, maxBytes int) ([]DiffToolEntry, bool) {
	for i := range entries {
		entries[i].DiffBytes = len(entries[i].Diff)
	}
	if maxBytes <= 0 {
		return entries, false
	}
	used := 0
	for i := range entries {
		size := len(entries[i].Diff)
		if used+size <= maxBytes {
			used += size
			continue
		}
		if i > 0 {
			return entries[:i], true
		}
		entries[0].Diff = cutAtLine(entries[0].Diff, maxBytes)
		entries[0].DiffTruncated = true
		return entries[:1], len(entries) > 1
	}
	return entries, false
}

// cutAtLine keeps at most maxBytes of text, ending on a whole line when one fits.
func cutAtLine(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := text[:maxBytes]
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		return cut[:idx+1]
	}
	return cut
}

// ShowToolResponse is the git_show tool wire shape.
type ShowToolResponse struct {
	Available bool           `json:"available"`
	Show      *GitShowResult `json:"show,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// BlameToolResponse is the git_blame tool wire shape.
type BlameToolResponse struct {
	Available bool           `json:"available"`
	Lines     []GitBlameLine `json:"lines,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// RefToolResponse is the git_ref tool wire shape.
type RefToolResponse struct {
	Available bool          `json:"available"`
	Refs      []GitRefEntry `json:"refs,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// BranchesToolResponse is the git_branches tool wire shape.
type BranchesToolResponse struct {
	Available bool             `json:"available"`
	Branches  []GitBranchEntry `json:"branches,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// RestoreToolResponse is the git_restore tool wire shape.
type RestoreToolResponse struct {
	Available bool     `json:"available"`
	Restored  []string `json:"restored,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// CommitStaging separates staged paths from excluded work.
type CommitStaging struct {
	Paths []string
	// Excluded is a bounded sample; ExcludedCount is exact.
	Excluded      []string
	ExcludedCount int
}

// CommitToolResponse is the git_commit tool wire shape.
type CommitToolResponse struct {
	Available bool     `json:"available"`
	Hash      string   `json:"hash,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	// UncommittedPaths samples excluded work; UncommittedCount is exact.
	UncommittedPaths []string `json:"uncommitted_paths,omitempty"`
	UncommittedCount int      `json:"uncommitted_count,omitempty"`
	Error            string   `json:"error,omitempty"`
	ExitCode         int      `json:"exit_code,omitempty"`
	OutputTail       string   `json:"output_tail,omitempty"`
	Hint             string   `json:"hint,omitempty"`
}

func MarshalShowToolResponse(show *GitShowResult, err error) (string, error) {
	if err != nil {
		return MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(ShowToolResponse{Available: true, Show: show})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func MarshalBlameToolResponse(lines []GitBlameLine, err error) (string, error) {
	if err != nil {
		return MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(BlameToolResponse{Available: true, Lines: lines})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func MarshalRefToolResponse(refs []GitRefEntry, err error) (string, error) {
	if err != nil {
		return MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(RefToolResponse{Available: true, Refs: refs})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func MarshalBranchesToolResponse(branches []GitBranchEntry, err error) (string, error) {
	if err != nil {
		return MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(BranchesToolResponse{Available: true, Branches: branches})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func MarshalRestoreToolResponse(restored []string, err error) (string, error) {
	if err != nil {
		return MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(RestoreToolResponse{Available: true, Restored: restored})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func MarshalCommitToolResponse(hash string, staging CommitStaging, err error) (string, error) {
	// A committed hash remains evidence when subsequent history recording fails.
	if hash != "" && err != nil {
		raw, marshalErr := surveyjson.Marshal(CommitToolResponse{
			Available: true, Hash: hash, Paths: staging.Paths, Error: err.Error(),
			UncommittedPaths: staging.Excluded, UncommittedCount: staging.ExcludedCount,
		})
		return string(raw), errors.Join(err, marshalErr)
	}
	if err != nil {
		var cf *CommitFailed
		if errors.As(err, &cf) {
			raw, mErr := surveyjson.Marshal(CommitToolResponse{
				Available:  false,
				Error:      cf.Summary(),
				ExitCode:   cf.ExitCode,
				OutputTail: cf.Tail(),
				Hint:       cf.Hint(),
			})
			if mErr != nil {
				return "", mErr
			}
			return string(raw), err
		}
		return MarshalToolFailure(err)
	}
	raw, err := surveyjson.Marshal(CommitToolResponse{
		Available:        true,
		Hash:             hash,
		Paths:            staging.Paths,
		UncommittedPaths: staging.Excluded,
		UncommittedCount: staging.ExcludedCount,
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
