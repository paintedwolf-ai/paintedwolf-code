package toolusage

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/logview"
)

// ProfileFromCaptureDir replays a debug capture with zero model calls.
func ProfileFromCaptureDir(captureDir string) (Profile, error) {
	return profileFromCapture(captureDir, nil, "replay")
}

// ProfileFromCaptureSession includes one root and its workers.
func ProfileFromCaptureSession(captureDir, rootSessionID string) (Profile, error) {
	rootSessionID = strings.TrimSpace(rootSessionID)
	if rootSessionID == "" {
		return Profile{}, fmt.Errorf("root session id is required")
	}
	return profileFromCapture(captureDir, []string{rootSessionID}, "live")
}

// ProfileFromCaptureSessions combines roots and their workers.
func ProfileFromCaptureSessions(captureDir string, rootSessionIDs []string) (Profile, error) {
	roots := make([]string, 0, len(rootSessionIDs))
	for _, root := range rootSessionIDs {
		if root = strings.TrimSpace(root); root != "" {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		return Profile{}, fmt.Errorf("at least one root session id is required")
	}
	return profileFromCapture(captureDir, roots, "live")
}

func profileFromCapture(captureDir string, rootSessionIDs []string, mode string) (Profile, error) {
	cap, err := logview.Resolve(captureDir)
	if err != nil {
		return Profile{}, err
	}
	sessions, err := cap.Sessions()
	if err != nil {
		return Profile{}, err
	}
	llm, err := cap.LLM()
	if err != nil {
		return Profile{}, err
	}
	if len(rootSessionIDs) > 0 {
		ids := sessionSubtreeIDs(rootSessionIDs, sessions)
		sessions = filterSessions(sessions, ids)
		llm = filterLLMRecords(llm, ids)
		if len(sessions) == 0 && len(llm) == 0 {
			return Profile{}, fmt.Errorf("sessions %q not found in capture %s", rootSessionIDs, cap.Dir)
		}
	}
	return AnalyzeCapture(mode, cap.Dir, sessions, llm), nil
}

func sessionSubtreeIDs(roots []string, sessions []logview.SessionRecord) map[string]struct{} {
	ids := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		ids[root] = struct{}{}
	}
	for {
		changed := false
		for _, s := range sessions {
			if s.SessionID == "" || s.ParentSessionID == "" {
				continue
			}
			if _, ok := ids[s.SessionID]; ok {
				continue
			}
			if _, parent := ids[s.ParentSessionID]; parent {
				ids[s.SessionID] = struct{}{}
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return ids
}

func filterSessions(sessions []logview.SessionRecord, ids map[string]struct{}) []logview.SessionRecord {
	if len(ids) == 0 {
		return nil
	}
	out := make([]logview.SessionRecord, 0, len(sessions))
	for _, s := range sessions {
		if _, ok := ids[s.SessionID]; ok {
			out = append(out, s)
		}
	}
	return out
}

func filterLLMRecords(llm []logview.LLMRecord, ids map[string]struct{}) []logview.LLMRecord {
	if len(ids) == 0 {
		return nil
	}
	out := make([]logview.LLMRecord, 0, len(llm))
	for _, rec := range llm {
		if _, ok := ids[rec.SessionID]; ok {
			out = append(out, rec)
		}
	}
	return out
}
