package portablepython

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/pythonevidence"
)

var (
	// ErrExecutionNotFound is returned when no persisted execution receipt exists
	// for the requested execution_id.
	ErrExecutionNotFound = errors.New("execution receipt not found")
	// ErrAmbiguousExecutionID is returned (fail-closed) when legacy or duplicate
	// persisted execution receipts share the same execution_id across distinct
	// session/actor/view scopes.
	ErrAmbiguousExecutionID = errors.New("ambiguous execution_id spans multiple scopes")
)

// FileReceiptStore is a durable, disk-backed implementation of
// pythonevidence.ReceiptSink that persists execution, admission, publication,
// and retained-artifact metadata receipts as JSONL files so they survive host
// restarts and can be reopened and projected for Den/Files inspection.
type FileReceiptStore struct {
	mu  sync.Mutex
	dir string
}

var _ pythonevidence.ReceiptSink = (*FileReceiptStore)(nil)

// NewFileReceiptStore creates or reopens a FileReceiptStore at dir.
func NewFileReceiptStore(dir string) (*FileReceiptStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("file receipt store requires non-empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create receipt store dir: %w", err)
	}
	return &FileReceiptStore{dir: dir}, nil
}

// Dir returns the on-disk directory backing this FileReceiptStore.
func (s *FileReceiptStore) Dir() string { return s.dir }

func (s *FileReceiptStore) PersistExecution(_ context.Context, exec pythonevidence.ExecutionRecord) error {
	return s.appendJSONL("executions.jsonl", exec)
}

func (s *FileReceiptStore) PersistAdmission(_ context.Context, adm pythonevidence.LedgerAdmission) error {
	return s.appendJSONL("admissions.jsonl", adm)
}

func (s *FileReceiptStore) PersistPublication(_ context.Context, pub pythonevidence.LedgerPublication) error {
	return s.appendJSONL("publications.jsonl", pub)
}

// PersistRetainedArtifact records a retained artifact summary (including its
// disk-backed StreamRef and ViewerScope identity) so it survives host reopen.
func (s *FileReceiptStore) PersistRetainedArtifact(_ context.Context, summary pythonevidence.RetainedArtifactSummary) error {
	return s.appendJSONL("retained.jsonl", summary)
}

func (s *FileReceiptStore) appendJSONL(filename string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	p := filepath.Join(s.dir, filename)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- fixed filename inside receipt store dir.
	if err != nil {
		return err
	}
	_, wErr := f.Write(raw)
	cErr := f.Close()
	if wErr != nil {
		return wErr
	}
	return cErr
}

// LoadExecutions reads all persisted ExecutionRecords from disk, returning the
// latest record per ExecutionID (merging start + completion records).
func (s *FileReceiptStore) LoadExecutions() ([]pythonevidence.ExecutionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := make(map[string]pythonevidence.ExecutionRecord)
	var order []string
	err := scanJSONL(filepath.Join(s.dir, "executions.jsonl"), func(line []byte) error {
		var rec pythonevidence.ExecutionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if _, seen := byID[rec.ExecutionID]; !seen {
			order = append(order, rec.ExecutionID)
		}
		byID[rec.ExecutionID] = rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]pythonevidence.ExecutionRecord, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}

// LoadAdmissions reads all persisted LedgerAdmissions for executionID (or all
// when executionID is empty).
func (s *FileReceiptStore) LoadAdmissions(executionID string) ([]pythonevidence.LedgerAdmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pythonevidence.LedgerAdmission
	err := scanJSONL(filepath.Join(s.dir, "admissions.jsonl"), func(line []byte) error {
		var rec pythonevidence.LedgerAdmission
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if executionID == "" || rec.Record.ExecutionID == executionID {
			out = append(out, rec)
		}
		return nil
	})
	return out, err
}

// LoadPublications reads all persisted LedgerPublications for executionID (or
// all when executionID is empty).
func (s *FileReceiptStore) LoadPublications(executionID string) ([]pythonevidence.LedgerPublication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pythonevidence.LedgerPublication
	err := scanJSONL(filepath.Join(s.dir, "publications.jsonl"), func(line []byte) error {
		var rec pythonevidence.LedgerPublication
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if executionID == "" || rec.Record.ExecutionID == executionID {
			out = append(out, rec)
		}
		return nil
	})
	return out, err
}

// LoadRetained looks up a persisted RetainedArtifactSummary by artifactID,
// failing closed if duplicate legacy records span multiple distinct scopes.
func (s *FileReceiptStore) LoadRetained(artifactID string) (pythonevidence.RetainedArtifactSummary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		found    pythonevidence.RetainedArtifactSummary
		ok       bool
		firstKey executionScopeKey
	)
	err := scanJSONL(filepath.Join(s.dir, "retained.jsonl"), func(line []byte) error {
		var rec pythonevidence.RetainedArtifactSummary
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if rec.ArtifactID != artifactID {
			return nil
		}
		key := executionScopeKey{
			sessionID:  strings.TrimSpace(rec.SessionID),
			checkoutID: strings.TrimSpace(rec.CheckoutID),
			actor:      strings.TrimSpace(rec.Actor),
			view:       strings.TrimSpace(rec.View),
		}
		if !ok {
			firstKey = key
			ok = true
		} else if key != firstKey {
			return fmt.Errorf("%w: artifact %s", ErrAmbiguousExecutionID, artifactID)
		}
		found = rec
		return nil
	})
	return found, ok, err
}

// ReadRetainedBytes enforces fail-closed ViewerScope authorization on a
// persisted retained artifact and reads its bytes from disk via port.
func (s *FileReceiptStore) ReadRetainedBytes(
	ctx context.Context,
	scope pythonevidence.ViewerScope,
	artifactID string,
	port pythonevidence.RetainedStreamPort,
) ([]byte, error) {
	if err := pythonevidence.ValidateViewerScope(scope); err != nil {
		return nil, err
	}
	if port == nil {
		return nil, pythonevidence.ErrMissingRetainedStreamPort
	}
	summary, ok, err := s.LoadRetained(artifactID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, pythonevidence.ErrArtifactNotFound
	}
	execRec := pythonevidence.ExecutionRecord{
		ExecutionID: summary.ExecutionID,
		SessionID:   summary.SessionID,
		CheckoutID:  summary.CheckoutID,
		Actor:       summary.Actor,
		View:        summary.View,
	}
	if !pythonevidence.AuthorizeExecutionAccess(execRec, scope) {
		return nil, pythonevidence.ErrAccessDenied
	}
	if summary.StreamRef == nil {
		return nil, pythonevidence.ErrArtifactNotFound
	}
	return port.LoadScreened(ctx, *summary.StreamRef)
}

type executionScopeKey struct {
	sessionID  string
	checkoutID string
	actor      string
	view       string
}

func scopeKeyForExecution(rec pythonevidence.ExecutionRecord) executionScopeKey {
	return executionScopeKey{
		sessionID:  strings.TrimSpace(rec.SessionID),
		checkoutID: strings.TrimSpace(rec.CheckoutID),
		actor:      strings.TrimSpace(rec.Actor),
		view:       strings.TrimSpace(rec.View),
	}
}

func (s *FileReceiptStore) loadExecutionByID(executionID string) (pythonevidence.ExecutionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		target   pythonevidence.ExecutionRecord
		found    bool
		firstKey executionScopeKey
	)
	err := scanJSONL(filepath.Join(s.dir, "executions.jsonl"), func(line []byte) error {
		var rec pythonevidence.ExecutionRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		if rec.ExecutionID != executionID {
			return nil
		}
		key := scopeKeyForExecution(rec)
		if !found {
			firstKey = key
			found = true
		} else if key != firstKey {
			return fmt.Errorf("%w: %s", ErrAmbiguousExecutionID, executionID)
		}
		target = rec
		return nil
	})
	if err != nil {
		return pythonevidence.ExecutionRecord{}, err
	}
	if !found {
		return pythonevidence.ExecutionRecord{}, fmt.Errorf("%w: %s", ErrExecutionNotFound, executionID)
	}
	return target, nil
}

// LoadAdmissionsForScope authorizes viewer access to executionID, rejects
// ambiguous cross-scope duplicate IDs, and returns only admissions matching the
// execution's authorized scope.
func (s *FileReceiptStore) LoadAdmissionsForScope(
	executionID string,
	scope pythonevidence.ViewerScope,
) ([]pythonevidence.LedgerAdmission, error) {
	if err := pythonevidence.ValidateViewerScope(scope); err != nil {
		return nil, err
	}
	target, err := s.loadExecutionByID(executionID)
	if err != nil {
		return nil, err
	}
	if !pythonevidence.AuthorizeExecutionAccess(target, scope) {
		return nil, pythonevidence.ErrAccessDenied
	}
	return s.loadAdmissionsForTarget(target)
}

func (s *FileReceiptStore) loadAdmissionsForTarget(target pythonevidence.ExecutionRecord) ([]pythonevidence.LedgerAdmission, error) {
	all, err := s.LoadAdmissions(target.ExecutionID)
	if err != nil {
		return nil, err
	}
	out := make([]pythonevidence.LedgerAdmission, 0, len(all))
	for _, adm := range all {
		if adm.Record.Actor != "" && target.Actor != "" && adm.Record.Actor != target.Actor {
			continue
		}
		if adm.Record.View != "" && target.View != "" && adm.Record.View != target.View {
			continue
		}
		out = append(out, adm)
	}
	return out, nil
}

// ProjectPersistedForDen reconstructs a visibility-scoped Den activity
// projection directly from the persisted on-disk JSONL receipts after reopen.
// Ambiguous cross-scope execution IDs fail closed, and store read/parse errors
// are propagated rather than returning an incomplete projection.
func (s *FileReceiptStore) ProjectPersistedForDen(
	executionID string,
	scope pythonevidence.ViewerScope,
) (pythonevidence.PythonActivityProjection, error) {
	target, err := s.loadExecutionByID(executionID)
	if err != nil {
		return pythonevidence.PythonActivityProjection{}, err
	}
	if !pythonevidence.AuthorizeExecutionAccess(target, scope) {
		return pythonevidence.PythonActivityProjection{
			ExecutionID:           target.ExecutionID,
			CallID:                target.CallID,
			Visible:               false,
			RedactedForVisibility: true,
		}, nil
	}
	ledger := pythonevidence.NewExecutionLedger(target, nil, nil)
	admissions, err := s.loadAdmissionsForTarget(target)
	if err != nil {
		return pythonevidence.PythonActivityProjection{}, err
	}
	for _, adm := range admissions {
		ledger.RecordAdmission(adm.Record, adm.ParentOperationID, nil)
	}
	pubs, err := s.LoadPublications(executionID)
	if err != nil {
		return pythonevidence.PythonActivityProjection{}, err
	}
	for _, pub := range pubs {
		_ = ledger.RecordPublication(pub.Record, pub.ParentOperationID, pub.Attribution.HostPublicationID, pub.Attribution.AttributionKind, nil, nil)
	}
	ledger.RecordCompletion(target.CleanCompletion, target.ExitCode, target.Stop)
	return pythonevidence.ProjectForDen(ledger, scope), nil
}

func scanJSONL(path string, fn func(line []byte) error) error {
	f, err := os.Open(path) // #nosec G304 -- internal receipt file inside FileReceiptStore.dir.
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := fn([]byte(line)); err != nil {
			return err
		}
	}
	return sc.Err()
}
