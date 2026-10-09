package sourceapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type commitReviewPath struct {
	rootID, rootAbs, head, path, status string
	submodule                           bool
}

func (p commitReviewPath) key() string { return p.rootID + "\x00" + p.path }

// commitReviewCursor continues after one root/path key of the Git status
// snapshot that answered the first page.
type commitReviewCursor struct {
	After    string `json:"after"`
	Snapshot string `json:"snapshot"`
}

var commitReviewPages = pagecursor.For[commitReviewCursor]("source_commit_review")

func commitReviewSnapshot(paths []commitReviewPath, roots []wire.SourceCommitRoot) (string, error) {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	if err := encoder.Encode(roots); err != nil {
		return "", fmt.Errorf("encode commit roots: %w", err)
	}
	for _, path := range paths {
		if err := encoder.Encode([]string{path.rootID, path.path, path.head, path.status}); err != nil {
			return "", fmt.Errorf("encode commit path: %w", err)
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// collectCommitPaths uses status as membership, including both sides of moves.
func (s *Review) collectCommitPaths(ctx context.Context, p *project.Project) ([]commitReviewPath, []wire.SourceCommitRoot) {
	lens := s.Comparisons.CommitLens(ctx, p)
	mgr := s.Git.Manager()
	byRoot := make(map[string]sourceledger.LensRoot)
	for _, root := range lens.Roots {
		byRoot[root.ID] = root
	}
	var paths []commitReviewPath
	var roots []wire.SourceCommitRoot
	for _, root := range project.RootRefsFrom(p) {
		state := wire.SourceCommitRoot{RootID: root.ID}
		bound, ok := byRoot[root.ID]
		if !ok {
			state.Error = "Git is unavailable for this folder."
			roots = append(roots, state)
			continue
		}
		status, err := mgr.CommitStatus(ctx, bound.Abs)
		if err != nil {
			state.Error = "Git could not read changes in this folder."
			roots = append(roots, state)
			continue
		}
		state.Available, state.Head = true, status.Head
		roots = append(roots, state)
		adapter := lens.Git.(gitWorkingTreeAdapter)
		prefix, _ := adapter.repoPrefix(bound.Abs)
		seen := make(map[string]bool)
		for _, file := range status.Files {
			locations := []string{file.Path}
			if strings.Contains(file.Status, "R") {
				locations = append(locations, file.FromPath)
			}
			for _, path := range locations {
				if path == "" {
					continue
				}
				rel, inside := strings.CutPrefix(path, prefix)
				if !inside || rel == "" || seen[rel] {
					continue
				}
				seen[rel] = true
				paths = append(paths, commitReviewPath{rootID: root.ID, rootAbs: bound.Abs, head: status.Head, path: rel, status: file.Status, submodule: strings.HasPrefix(file.Submodule, "S")})
			}
		}
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].key() < paths[j].key() })
	return paths, roots
}

// writeCommitReview pages Git status paths; scope binds cursors to the
// project workspace the first page read.
func (s *Review) writeCommitReview(w http.ResponseWriter, r *http.Request, p *project.Project, scope string, page httpio.PageQuery) {
	paths, roots := s.collectCommitPaths(r.Context(), p)
	out := MapSourceWalk(sourceledger.WalkResult{Baseline: sourceledger.Baseline{Kind: sourceledger.BaselineCommit}})
	out.CommitRoots = roots
	for _, root := range roots {
		out.CommitAvailable = out.CommitAvailable || root.Available
	}
	snapshot, err := commitReviewSnapshot(paths, roots)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	cursorState := commitReviewCursor{Snapshot: snapshot}
	if page.Cursor != "" {
		previous, err := commitReviewPages.Decode(page.Cursor, scope)
		if err == nil && (!strings.Contains(previous.After, "\x00") || previous.Snapshot == "") {
			err = pagecursor.ErrInvalid
		}
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		if previous.Snapshot != cursorState.Snapshot {
			s.responses.Fail(w, wire.ApiErrorCodeSourceHistoryChanged, "Git changes moved while loading. Refresh the comparison.")
			return
		}
		cursorState.After = previous.After
	}
	start := sort.Search(len(paths), func(i int) bool { return paths[i].key() > cursorState.After })
	end := min(start+page.Limit, len(paths))
	oids, err := s.commitPageOIDs(r.Context(), paths[start:end])
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	for _, path := range paths[start:end] {
		working := readWorkingCommit(p, path.rootID, path.rootAbs, path.path)
		history, err := s.SourceLedger.History.ReviewPathHistory(r.Context(), p.ID, p.BranchForRoot(path.rootID), path.rootID, path.path, working.Side.State == "absent")
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		out.Files = append(out.Files, commitReviewFile(path, oids[path.key()], working, history))
		for _, command := range history.Commands {
			present := false
			for _, listed := range out.Commands {
				if listed.ID == command.ID {
					present = true
					break
				}
			}
			if !present {
				out.Commands = append(out.Commands, *mapSourceCommandWindow(command))
			}
		}
	}
	if end < len(paths) {
		cursorState.After = paths[end-1].key()
		out.NextCursor, err = commitReviewPages.Encode(scope, cursorState)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func commitReviewFile(path commitReviewPath, before string, working commitWorkingState, history sourceledger.ReviewHistory) wire.SourceWalkFile {
	after := working.Side
	op := wire.SourceChangeOpWrite
	if after.State == "absent" {
		op = wire.SourceChangeOpDelete
	} else if before == "" {
		op = wire.SourceChangeOpCreate
	}
	if path.submodule {
		op = wire.SourceChangeOpWrite
	}
	file := wire.SourceWalkFile{
		FileID: history.FileID, RootID: path.rootID, Path: path.path,
		PresentationEffectID: history.PresentationEffectID, PresentationOrdinal: history.PresentationOrdinal,
		ChangedSincePresented: history.ChangedSincePresented, UnpresentedAgentEffects: history.UnpresentedAgentEffects,
		Tip:       wire.SourceTip{State: wire.SourceTipStateContent, Sha256: after.SHA256},
		HeadMatch: wire.SourceHeadMatchUnknown, Effects: make([]wire.SourceWalkEffect, 0, len(history.Effects)),
		Commit: &wire.SourceCommitComparison{Head: path.head, Status: path.status, Op: op, Availability: string(after.Availability), HistoryTruncated: history.Truncated},
	}
	if after.State == "absent" {
		file.Tip.State = wire.SourceTipStateAbsent
	}
	switch {
	case path.submodule:
	case after.State == "absent" && before == "":
		file.HeadMatch = wire.SourceHeadMatchSame
	case before == "":
		file.HeadMatch = wire.SourceHeadMatchAbsent
	case after.State == "absent":
		file.HeadMatch = wire.SourceHeadMatchDiffers
	case working.OIDs.SHA1 == before || working.OIDs.SHA256 == before:
		file.HeadMatch = wire.SourceHeadMatchSame
	case working.OIDs.SHA1 != "":
		file.HeadMatch = wire.SourceHeadMatchDiffers
	}
	for _, effect := range history.Effects {
		row := MapSourceEffect(effect)
		for _, command := range history.Commands {
			if command.ID == effect.CommandWindowID {
				id := command.ID
				row.CommandID = &id
				break
			}
		}
		file.Effects = append(file.Effects, row)
	}
	return file
}

func (s *Review) commitPageOIDs(ctx context.Context, paths []commitReviewPath) (map[string]string, error) {
	mgr := s.Git.Manager()
	out := make(map[string]string)
	for start := 0; start < len(paths); {
		root := paths[start]
		end := start
		var requested []string
		for end < len(paths) && paths[end].rootID == root.rootID {
			requested = append(requested, paths[end].path)
			end++
		}
		if root.head != "" {
			oids, err := mgr.TreeOIDs(ctx, root.rootAbs, root.head, requested)
			if err != nil {
				return nil, err
			}
			for path, oid := range oids {
				out[root.rootID+"\x00"+path] = oid
			}
		}
		start = end
	}
	return out, nil
}

func commitPathOID(ctx context.Context, mgr git.GitManager, rootAbs, head, path string) (string, error) {
	if head == "" {
		return "", nil
	}
	if !filepath.IsLocal(path) || path == "." {
		return "", fmt.Errorf("invalid commit path")
	}
	oids, err := mgr.TreeOIDs(ctx, rootAbs, head, []string{path})
	return oids[path], err
}
