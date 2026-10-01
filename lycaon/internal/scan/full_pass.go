package scan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// recentFullPasses bounds how many of a root's passes one read considers.
const recentFullPasses = 16

// Full-pass members dispatch together to read one source generation.
type FullPass struct {
	ID            string
	CanonicalPath string
	Trigger       api.ScanTrigger
	RequestedAt   time.Time
	// StartedAt is zero until the members are dispatched.
	StartedAt time.Time
	Members   []FullPassMember
}

type FullPassMember struct {
	ScannerID string
	Phase     api.FullPassMemberPhase
	Scan      *api.CodeScan
}

func (p FullPass) Started() bool { return !p.StartedAt.IsZero() }

// Finished reports that no member waits and every started member's scan is terminal.
func (p FullPass) Finished() bool {
	for _, member := range p.Members {
		switch member.Phase {
		case api.FullPassMemberWaitingForScanner, api.FullPassMemberWaitingForPass:
			return false
		case api.FullPassMemberStarted:
			if member.Scan == nil || !StatusTerminal(member.Scan.Status) {
				return false
			}
		case api.FullPassMemberNotStarted:
		}
	}
	return true
}

func (p FullPass) Covers(scannerIDs []string) bool {
	runs := make(map[string]bool, len(p.Members))
	for _, member := range p.Members {
		runs[member.ScannerID] = member.Phase != api.FullPassMemberNotStarted
	}
	for _, id := range scannerIDs {
		if !runs[id] {
			return false
		}
	}
	return true
}

func (p FullPass) ScannerIDs() []string {
	out := make([]string, 0, len(p.Members))
	for _, member := range p.Members {
		out = append(out, member.ScannerID)
	}
	return out
}

func (p FullPass) ScanIDs() []string {
	out := make([]string, 0, len(p.Members))
	for _, member := range p.Members {
		if member.Scan != nil {
			out = append(out, member.Scan.ID)
		}
	}
	return out
}

// completedAt is the last member completion, or when the pass settled
// without any member running.
func (p FullPass) completedAt() *time.Time {
	var latest *time.Time
	for _, member := range p.Members {
		if member.Scan == nil || member.Scan.CompletedAt == nil {
			continue
		}
		if latest == nil || member.Scan.CompletedAt.After(*latest) {
			at := *member.Scan.CompletedAt
			latest = &at
		}
	}
	at := p.StartedAt
	if at.IsZero() {
		at = p.RequestedAt
	}
	if latest != nil && latest.After(at) {
		return latest
	}
	return &at
}

func (p FullPass) Wire() api.SecurityFullPass {
	out := api.SecurityFullPass{
		AssessmentID: p.ID, RequestedAt: p.RequestedAt, Trigger: p.Trigger,
		Members: make([]api.SecurityFullPassMember, 0, len(p.Members)),
	}
	if p.Started() {
		at := p.StartedAt
		out.StartedAt = &at
	}
	for _, member := range p.Members {
		out.Members = append(out.Members, api.SecurityFullPassMember{
			ScannerID: member.ScannerID, Phase: member.Phase, Scan: member.Scan,
		})
	}
	if p.Finished() {
		out.CompletedAt = p.completedAt()
		out.CoverageStatus = passCoverage(p.Members)
	}
	return out
}

// Any incomplete member limits coverage, including scanners that never started.
func passCoverage(members []FullPassMember) api.ScanCoverageStatus {
	out := api.ScanCoverageComplete
	established := false
	for _, member := range members {
		coverage := api.ScanCoveragePartial
		if scan := member.Scan; scan != nil && scan.Status == api.CodeScanStatusComplete &&
			scan.CoverageStatus != "" && scan.CoverageStatus != api.ScanCoverageUnavailable {
			coverage = scan.CoverageStatus
			established = true
		}
		if coverageRank(coverage) < coverageRank(out) {
			out = coverage
		}
	}
	if !established {
		return api.ScanCoverageUnavailable
	}
	return out
}

// Progress comes from the bound scan or the series that still owes it.
func fullPassMemberPhase(passID string, scan *api.CodeScan, series SeriesRow, known, busy bool) api.FullPassMemberPhase {
	switch {
	case scan != nil:
		return api.FullPassMemberStarted
	case !known || !series.OwesPass(passID):
		return api.FullPassMemberNotStarted
	case series.DispatchPassID == passID:
		return api.FullPassMemberWaitingForPass
	case busy || series.DispatchToken != "":
		return api.FullPassMemberWaitingForScanner
	default:
		return api.FullPassMemberWaitingForPass
	}
}

type FullPassDraft struct {
	ID            string
	CanonicalPath string
	Scanners      []string
	Trigger       api.ScanTrigger
	RequestedAt   time.Time
}

func (s *SQLStore) InsertFullPass(ctx context.Context, draft FullPassDraft) error {
	scannersJSON, err := surveyjson.Marshal(UniqueSortedStrings(draft.Scanners))
	if err != nil {
		return err
	}
	return s.queries.InsertFullPass(ctx, db.InsertFullPassParams{
		ID: draft.ID, CanonicalPath: draft.CanonicalPath, ScannersJson: string(scannersJSON),
		Trigger: string(draft.Trigger), RequestedAt: db.FormatTime(draft.RequestedAt.UTC()),
	})
}

func (s *SQLStore) ReadFullPassRequest(ctx context.Context, id string) (FullPassDraft, error) {
	row, err := s.queries.GetFullPass(ctx, id)
	if err != nil {
		return FullPassDraft{}, fmt.Errorf("read full pass %s: %w", id, err)
	}
	var scanners []string
	if err := json.Unmarshal([]byte(row.ScannersJson), &scanners); err != nil {
		return FullPassDraft{}, fmt.Errorf("full pass %s scanners: %w", id, err)
	}
	return FullPassDraft{
		ID: row.ID, CanonicalPath: row.CanonicalPath, Scanners: scanners,
		Trigger: api.ScanTrigger(row.Trigger), RequestedAt: parseCadenceTime(row.RequestedAt),
	}, nil
}

// WidenFullPass sets a pass's scanners while it has not started; false once it has.
func (s *SQLStore) WidenFullPass(ctx context.Context, id string, scanners []string) (bool, error) {
	scannersJSON, err := surveyjson.Marshal(UniqueSortedStrings(scanners))
	if err != nil {
		return false, err
	}
	changed, err := s.queries.WidenFullPass(ctx, db.WidenFullPassParams{ScannersJson: string(scannersJSON), ID: id})
	return changed > 0, err
}

func (s *SQLStore) MarkFullPassStarted(ctx context.Context, id string, at time.Time) error {
	_, err := s.queries.MarkFullPassStarted(ctx, db.MarkFullPassStartedParams{StartedAt: db.FormatTime(at.UTC()), ID: id})
	return err
}

// Requesters joining a running pass inherit its existing evidence bindings.
func (s *SQLStore) BindFullPassRequester(ctx context.Context, passID string, bind FullScanContext) error {
	sessionID := strings.TrimSpace(bind.SessionID)
	workflowRunID := strings.TrimSpace(bind.WorkflowRunID)
	if sessionID == "" && workflowRunID == "" {
		return nil
	}
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		now := time.Now().UTC()
		if sessionID != "" {
			if err := qtx.BindFullPassSession(ctx, db.BindFullPassSessionParams{
				PassID: passID, SessionID: sessionID, CreatedAt: db.FormatTime(now),
			}); err != nil {
				return err
			}
		}
		if workflowRunID != "" {
			if err := qtx.BindFullPassWorkflowRun(ctx, db.BindFullPassWorkflowRunParams{
				PassID: passID, WorkflowRunID: workflowRunID, CreatedAt: db.FormatTime(now),
			}); err != nil {
				return err
			}
		}
		scans, err := qtx.ListFullPassScans(ctx, passID)
		if err != nil {
			return err
		}
		for _, scan := range scans {
			changed, err := bindScanContextsTx(ctx, qtx, scan.ID, "", workflowRunID, sessionID, now)
			if err != nil {
				return err
			}
			if changed {
				if err := s.emitScanTx(ctx, tx, scan.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *SQLStore) BindFullPassScan(ctx context.Context, passID, scanID string) error {
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		sessions, err := qtx.ListFullPassSessions(ctx, passID)
		if err != nil {
			return err
		}
		workflowRuns, err := qtx.ListFullPassWorkflowRuns(ctx, passID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		changed := false
		for _, sessionID := range sessions {
			bound, err := bindScanContextsTx(ctx, qtx, scanID, "", "", sessionID, now)
			if err != nil {
				return err
			}
			changed = changed || bound
		}
		for _, workflowRunID := range workflowRuns {
			bound, err := bindScanContextsTx(ctx, qtx, scanID, "", workflowRunID, "", now)
			if err != nil {
				return err
			}
			changed = changed || bound
		}
		if !changed {
			return nil
		}
		return s.emitScanTx(ctx, tx, scanID)
	})
}

// FullPass reads one pass with its members' progress; nil when unknown.
func (s *SQLStore) FullPass(ctx context.Context, id string) (*FullPass, error) {
	row, err := s.queries.GetFullPass(ctx, strings.TrimSpace(id))
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	passes, err := s.fullPasses(ctx, []db.SecurityFullPasses{row})
	if err != nil {
		return nil, err
	}
	return &passes[0], nil
}

// FullPassesForPath returns a root's recent passes, newest first.
func (s *SQLStore) FullPassesForPath(ctx context.Context, canonicalPath string) ([]FullPass, error) {
	rows, err := s.queries.ListFullPassesForPath(ctx, db.ListFullPassesForPathParams{
		CanonicalPath: canonicalPath, MaxPasses: recentFullPasses,
	})
	if err != nil {
		return nil, err
	}
	return s.fullPasses(ctx, rows)
}

// FullPassesForWorkflowRun returns the passes a workflow run asked for, newest first.
func (s *SQLStore) FullPassesForWorkflowRun(ctx context.Context, workflowRunID string) ([]FullPass, error) {
	workflowRunID = strings.TrimSpace(workflowRunID)
	if workflowRunID == "" {
		return nil, nil
	}
	rows, err := s.queries.ListFullPassesForWorkflowRun(ctx, workflowRunID)
	if err != nil {
		return nil, err
	}
	return s.fullPasses(ctx, rows)
}

// FullPassOwed reports whether any scanner of the root still owes a pass its scan.
func (s *SQLStore) FullPassOwed(ctx context.Context, canonicalPath string) (bool, error) {
	owed, err := s.queries.FullPassOwedForPath(ctx, canonicalPath)
	return owed != 0, err
}

// FullPassOwedForSession reports whether a full pass the session requested still has
// scanners waiting to start or finish.
func (s *SQLStore) FullPassOwedForSession(ctx context.Context, sessionID string) (bool, error) {
	owed, err := s.queries.FullPassOwedForSession(ctx, strings.TrimSpace(sessionID))
	return owed != 0, err
}

// fullPassRoot is the series state every pass of one root reads its waiting members from.
type fullPassRoot struct {
	series map[string]SeriesRow
	busy   map[string]bool
}

func (s *SQLStore) loadFullPassRoot(ctx context.Context, canonicalPath string) (fullPassRoot, error) {
	series, err := s.ListSeriesForPath(ctx, canonicalPath)
	if err != nil {
		return fullPassRoot{}, err
	}
	open, err := s.queries.OpenScansForPath(ctx, canonicalPath)
	if err != nil {
		return fullPassRoot{}, err
	}
	root := fullPassRoot{series: make(map[string]SeriesRow, len(series)), busy: make(map[string]bool, len(open))}
	for _, row := range series {
		root.series[row.ScannerID] = row
	}
	for _, scan := range open {
		root.busy[db.StringFromNull(scan.ScannerID)] = true
	}
	return root, nil
}

func (s *SQLStore) fullPasses(ctx context.Context, rows []db.SecurityFullPasses) ([]FullPass, error) {
	roots := make(map[string]fullPassRoot)
	out := make([]FullPass, 0, len(rows))
	for _, row := range rows {
		root, loaded := roots[row.CanonicalPath]
		if !loaded {
			var err error
			if root, err = s.loadFullPassRoot(ctx, row.CanonicalPath); err != nil {
				return nil, err
			}
			roots[row.CanonicalPath] = root
		}
		pass, err := s.readFullPass(ctx, row, root)
		if err != nil {
			return nil, err
		}
		out = append(out, pass)
	}
	return out, nil
}

func (s *SQLStore) readFullPass(ctx context.Context, row db.SecurityFullPasses, root fullPassRoot) (FullPass, error) {
	var scanners []string
	if err := json.Unmarshal([]byte(row.ScannersJson), &scanners); err != nil {
		return FullPass{}, fmt.Errorf("full pass %s scanners: %w", row.ID, err)
	}
	scanRows, err := s.queries.ListFullPassScans(ctx, row.ID)
	if err != nil {
		return FullPass{}, err
	}
	// Rows arrive newest first; a replacement run supersedes its predecessor.
	newest := make(map[string]*api.CodeScan, len(scanRows))
	for _, scanRow := range scanRows {
		scan := codeScanFromRow(scanRow)
		if _, seen := newest[scan.ScannerID]; seen {
			continue
		}
		if err := hydrateScanFacts(ctx, s.db, scan); err != nil {
			return FullPass{}, err
		}
		// A reused execution is projected in the pass that requested this read.
		scan.AssessmentID = row.ID
		newest[scan.ScannerID] = scan
	}
	pass := FullPass{
		ID: row.ID, CanonicalPath: row.CanonicalPath, Trigger: api.ScanTrigger(row.Trigger),
		RequestedAt: parseCadenceTime(row.RequestedAt), StartedAt: parseCadenceTime(row.StartedAt),
		Members: make([]FullPassMember, 0, len(scanners)),
	}
	for _, scannerID := range scanners {
		series, known := root.series[scannerID]
		scan := newest[scannerID]
		pass.Members = append(pass.Members, FullPassMember{
			ScannerID: scannerID,
			Phase:     fullPassMemberPhase(row.ID, scan, series, known, root.busy[scannerID]),
			Scan:      scan,
		})
	}
	return pass, nil
}

// ErrSecurityScannersOff means the device has security scanning turned off.
var ErrSecurityScannersOff = errors.New("security scanners are off")

// ErrNoScannerAvailable means no selected scanner can run a full pass for the root.
var ErrNoScannerAvailable = errors.New("no selected scanner is available for this project")

// FullScanContext binds a full pass to who asked for it.
type FullScanContext struct {
	SessionID     string
	WorkflowRunID string
}

type FullScanRequester interface {
	RequestFull(ctx context.Context, projectDir string, scannerIDs []string, trigger api.ScanTrigger, bind FullScanContext) (FullPass, error)
}
