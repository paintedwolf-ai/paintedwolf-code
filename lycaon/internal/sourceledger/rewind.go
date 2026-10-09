package sourceledger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// RewindIssue identifies a conflict in the selected source suffix.
type RewindIssue struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
	Code   string `json:"code"`
}

type RewindBlockedError struct{ Issues []RewindIssue }

func (e *RewindBlockedError) Error() string {
	if len(e.Issues) == 0 {
		return "rewind is unavailable"
	}
	return fmt.Sprintf("rewind blocked at %s: %s", e.Issues[0].Path, e.Issues[0].Code)
}

// RewindFile joins all selected effects on one logical file and branch. Expected
// is the final selected state; Target precedes the first selected effect.
type RewindFile struct {
	FileID             string
	ActorPersonID      string
	DocumentID         string
	DocumentRevision   int64
	DocumentCheckpoint []byte
	BranchID           sourcebranch.ID
	Expected           RestorableVersion
	Target             RestorableVersion
}

type RewindPlan struct {
	Files  []RewindFile
	Issues []RewindIssue
}

type rewindEffect struct {
	fileID, branch, before, after, rootID, path, landing string
	shared                                               bool
	observedCommand                                      bool
}

// PlanRewind selects source effects by permanent opener identity.
func (s *Comparisons) PlanRewind(ctx context.Context, projectID, sessionID string, openingMessageIDs []string) (RewindPlan, error) {
	effects, err := s.readRewindEffects(ctx, projectID, sessionID, openingMessageIDs)
	if err != nil {
		return RewindPlan{}, err
	}
	if len(effects) > 10000 {
		return RewindPlan{Files: []RewindFile{}, Issues: []RewindIssue{{Code: "history_limit"}}}, nil
	}
	return s.planRewindEffects(ctx, projectID, effects)
}

func (s *Comparisons) readRewindEffects(ctx context.Context, projectID, sessionID string, openingMessageIDs []string) ([]rewindEffect, error) {
	encoded, err := json.Marshal(openingMessageIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.sqlDB.QueryContext(ctx, `WITH selected AS MATERIALIZED (
      SELECT session_id,turn FROM session_source_turns
      WHERE session_id=? AND opening_message_id IN (SELECT value FROM json_each(?))
    )
    SELECT e.file_id,o.branch_id,COALESCE(e.before_version_id,''),e.after_version_id,e.root_id,e.path,v.landing,
      EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id
        AND (a.origin <> 'agent' OR NOT EXISTS (SELECT 1 FROM selected t WHERE a.session_id=t.session_id AND a.turn=t.turn))),
      (o.origin='external' AND o.command_window_id IS NOT NULL)
    FROM source_effects e JOIN source_operations o ON o.id=e.operation_id
    JOIN source_versions v ON v.id=e.after_version_id
    WHERE e.project_id=? AND EXISTS (
      SELECT 1 FROM source_effect_authors a JOIN selected t ON t.session_id=a.session_id AND t.turn=a.turn
      WHERE a.effect_id=e.id AND (a.origin='agent' OR o.command_window_id IS NOT NULL)
    ) ORDER BY e.ordinal LIMIT 10001`, sessionID, string(encoded), projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	effects := []rewindEffect{}
	for rows.Next() {
		var effect rewindEffect
		if err := rows.Scan(&effect.fileID, &effect.branch, &effect.before, &effect.after, &effect.rootID, &effect.path, &effect.landing, &effect.shared, &effect.observedCommand); err != nil {
			return nil, err
		}
		effects = append(effects, effect)
	}
	err = rows.Err()
	if err != nil {
		return nil, err
	}
	return effects, nil
}

func (s *Comparisons) planRewindEffects(ctx context.Context, projectID string, effects []rewindEffect) (RewindPlan, error) {
	out := RewindPlan{Files: []RewindFile{}, Issues: []RewindIssue{}}
	indexes := map[string]int{}
	retainedBytes := 0
	for _, e := range effects {
		branch := sourcebranch.ID(e.branch)
		// Promotions have separate effects on the destination branch.
		if branch.IsWorker() {
			continue
		}
		issue := func(code string) {
			out.Issues = append(out.Issues, RewindIssue{RootID: e.rootID, Path: e.path, Code: code})
		}
		if e.shared {
			issue("shared_contribution")
		}
		if e.observedCommand {
			issue("command_authorship_unavailable")
		}
		if e.landing != LandingWorkingFile {
			issue("unsaved_agent_change")
			continue
		}
		after, err := s.history.ReadRestorableVersion(ctx, projectID, e.after)
		if err != nil {
			issue("version_unavailable")
			continue
		}
		before := RestorableVersion{FileID: e.fileID, ProjectID: projectID, RootID: after.RootID, Path: after.Path, State: "absent"}
		if e.before != "" {
			before, err = s.history.ReadRestorableVersion(ctx, projectID, e.before)
			if err != nil {
				issue("version_unavailable")
				continue
			}
		}
		retainedBytes += len(before.Content) + len(after.Content)
		if retainedBytes > 64<<20 {
			issue("history_limit")
			break
		}
		key := e.branch + "\x00" + e.fileID
		if i, ok := indexes[key]; ok {
			previous := &out.Files[i]
			if !sameRestorableState(previous.Expected, before) {
				issue("intervening_change")
			}
			previous.Expected = after
		} else {
			if len(out.Files) >= 500 {
				issue("history_limit")
				break
			}
			indexes[key] = len(out.Files)
			out.Files = append(out.Files, RewindFile{FileID: e.fileID, BranchID: branch, Expected: after, Target: before})
		}
	}
	for index := range out.Files {
		if err := s.checkRewindHead(ctx, projectID, &out, &out.Files[index]); err != nil {
			return RewindPlan{}, err
		}
	}

	return out, nil
}

func sameRestorableState(a, b RestorableVersion) bool {
	return a.State == b.State && a.SHA256 == b.SHA256 && a.RootID == b.RootID && a.Path == b.Path
}

func (s *Comparisons) checkRewindHead(ctx context.Context, projectID string, out *RewindPlan, file *RewindFile) error {

	head, err := s.history.ResolveHeadByFile(ctx, projectID, file.BranchID, file.FileID)
	if err != nil {
		return err
	}
	if head.Path != file.Expected.Path || head.RootID != file.Expected.RootID || head.State != file.Expected.State {
		out.Issues = append(out.Issues, RewindIssue{RootID: file.Expected.RootID, Path: file.Expected.Path, Code: "later_change"})
		return nil
	}
	equivalent := false
	if head.VersionID != file.Expected.ID {
		var derived, cause string
		err := s.sqlDB.QueryRowContext(ctx, `SELECT COALESCE(v.derived_from_version_id,''),COALESCE(o.cause,'') FROM source_versions v LEFT JOIN source_operations o ON o.id=v.operation_id WHERE v.id=?`, head.VersionID).Scan(&derived, &cause)
		if err != nil {
			return err
		}
		equivalent = derived == file.Expected.ID && (cause == "session_rewind" || cause == "session_rewind_rollback") && head.State == file.Expected.State && head.SHA256 == file.Expected.SHA256 && head.Path == file.Expected.Path && head.RootID == file.Expected.RootID
	}
	if head.VersionID != file.Expected.ID && !equivalent {
		restored, err := s.rewindCompensationTarget(ctx, projectID, head.VersionID, file.Target.ID)
		if err != nil {
			return err
		}
		if restored != nil {
			current, err := s.history.ReadRestorableVersion(ctx, projectID, head.VersionID)
			if err != nil {
				return err
			}
			file.Expected = current
			file.Target.Content = restored.Content
			file.Target.SHA256 = restored.SHA256
			kept := out.Issues[:0]
			for _, issue := range out.Issues {
				if issue.RootID == file.Expected.RootID && issue.Path == file.Expected.Path && (issue.Code == "shared_contribution" || issue.Code == "intervening_change") {
					continue
				}
				kept = append(kept, issue)
			}
			out.Issues = kept
			return nil
		}
	}
	if head.VersionID != file.Expected.ID && !equivalent {
		out.Issues = append(out.Issues, RewindIssue{RootID: file.Expected.RootID, Path: file.Expected.Path, Code: "later_change"})
	}
	return nil
}
