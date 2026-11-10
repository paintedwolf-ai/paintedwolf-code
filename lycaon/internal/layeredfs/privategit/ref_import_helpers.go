package privategit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
)

func buildPlannedImportRefs(
	jobID, checkpointID string,
	cp *CheckpointGitState,
	req RefImportRequest,
) (map[string]string, map[string]string, error) {
	planned := make(map[string]string)
	srcToDest := make(map[string]string)

	filter := make(map[string]bool, len(req.RefFilter))
	for _, rf := range req.RefFilter {
		rf = strings.TrimSpace(rf)
		if rf == "" {
			continue
		}
		filter[rf] = true
		if !strings.HasPrefix(rf, "refs/") && rf != "HEAD" {
			filter["refs/heads/"+rf] = true
		}
	}

	for ref, oid := range cp.Refs {
		if len(filter) > 0 && !filter[ref] {
			continue
		}
		if !req.Options.IncludeUnchanged && len(filter) == 0 {
			if initOID, had := cp.InitialRefs[ref]; had && initOID == oid {
				continue
			}
		}
		dest, err := NamespacedRef(jobID, checkpointID, ref)
		if err != nil {
			return nil, nil, err
		}
		planned[dest] = oid
		srcToDest[ref] = dest
	}

	if (cp.DetachedHead || req.Options.IncludeDetachedHead || len(planned) == 0) && cp.HeadOID != "" {
		if len(filter) == 0 || filter["HEAD"] {
			dest, err := NamespacedRef(jobID, checkpointID, "HEAD")
			if err != nil {
				return nil, nil, err
			}
			planned[dest] = cp.HeadOID
			srcToDest["HEAD"] = dest
		}
	}

	if req.Options.IncludeTags {
		for tagRef, oid := range cp.Tags {
			if len(filter) > 0 && !filter[tagRef] {
				continue
			}
			dest, err := NamespacedRef(jobID, checkpointID, tagRef)
			if err != nil {
				return nil, nil, err
			}
			planned[dest] = oid
			srcToDest[tagRef] = dest
		}
	}

	if req.Options.IncludeStashes {
		for idx, st := range cp.Stashes {
			stashKey := fmt.Sprintf("stash/%d", idx)
			dest, err := NamespacedRef(jobID, checkpointID, stashKey)
			if err != nil {
				return nil, nil, err
			}
			planned[dest] = st.OID
			srcToDest[st.Ref] = dest
		}
	}

	return planned, srcToDest, nil
}

func prepareImportJournalRecord(
	journalPath, jobID, workerID, checkpointID, trunkDir string,
	planned map[string]string,
) (ImportJournalRecord, error) {
	raw, err := os.ReadFile(journalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ImportJournalRecord{
				JobID:        jobID,
				WorkerID:     workerID,
				CheckpointID: checkpointID,
				TrunkDir:     trunkDir,
				Stage:        StageIntent,
				PlannedRefs:  cloneStringMap(planned),
				Published:    make(map[string]string),
				UpdatedAt:    time.Now(),
			}, nil
		}
		return ImportJournalRecord{}, err
	}
	var existing ImportJournalRecord
	if err := json.Unmarshal(raw, &existing); err != nil {
		return ImportJournalRecord{}, err
	}
	if len(existing.PlannedRefs) != len(planned) {
		return ImportJournalRecord{}, fmt.Errorf("%w: journal planned ref count mismatch", ErrRefImportConflict)
	}
	for k, v := range planned {
		if existing.PlannedRefs[k] != v {
			return ImportJournalRecord{}, fmt.Errorf("%w: journal planned ref %s has %s, want %s", ErrRefImportConflict, k, existing.PlannedRefs[k], v)
		}
	}
	if existing.Published == nil {
		existing.Published = make(map[string]string)
	}
	return existing, nil
}

func checkTrunkRefConflicts(trunkDir string, planned map[string]string) error {
	commonDir := resolveTrunkCommonGitDir(trunkDir)
	for destRef, wantOID := range planned {
		existing := resolveRefInGitDir(commonDir, destRef)
		if existing != "" && existing != wantOID {
			return fmt.Errorf("%w: destination ref %s already exists with %s (want %s)", ErrRefImportConflict, destRef, existing, wantOID)
		}
	}
	return nil
}

func saveImportJournalRecord(journalPath string, rec ImportJournalRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(journalPath),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

func verifyImportedTipsInTrunk(ctx context.Context, trunkDir string, planned map[string]string) error {
	if len(planned) == 0 {
		return nil
	}
	var stdinBuf bytes.Buffer
	seen := make(map[string]struct{})
	for _, oid := range planned {
		if _, ok := seen[oid]; !ok {
			seen[oid] = struct{}{}
			stdinBuf.WriteString(oid)
			stdinBuf.WriteByte('\n')
		}
	}
	out, code, err := gitexec.RunTo(ctx, trunkDir, []string{"rev-list", "--objects", "--stdin"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		Input:   stdinBuf.Bytes(),
	}, io.Discard)
	if err != nil || code != 0 {
		return fmt.Errorf("%w: trunk object verification failed (exit %d): %v (%s)", ErrCorruptObjectStore, code, err, strings.TrimSpace(string(out)))
	}
	return nil
}

type revListObjectSink struct {
	rem  []byte
	seen map[string]struct{}
	oids []string
}

func (s *revListObjectSink) Write(p []byte) (int, error) {
	s.rem = append(s.rem, p...)
	for {
		nl := bytes.IndexByte(s.rem, '\n')
		if nl < 0 {
			break
		}
		s.parseLine(s.rem[:nl])
		s.rem = s.rem[nl+1:]
	}
	return len(p), nil
}

func (s *revListObjectSink) flush() {
	if len(s.rem) > 0 {
		s.parseLine(s.rem)
		s.rem = nil
	}
}

func (s *revListObjectSink) parseLine(line []byte) {
	fields := bytes.Fields(line)
	if len(fields) > 0 && len(fields[0]) == 40 {
		oid := strings.ToLower(string(fields[0]))
		if isHexString(oid) {
			if _, ok := s.seen[oid]; !ok {
				s.seen[oid] = struct{}{}
				s.oids = append(s.oids, oid)
			}
		}
	}
}

func cloneStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
