package workerroster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// ShownQuestions remembers, per coordinator session, the pending questions
// the latest request showed. An answer must name a question its request
// showed, so one written before the question arrived, or a second answer in
// the same batch, is refused.
type ShownQuestions struct {
	mu        sync.Mutex
	bySession map[string]map[string]string
}

// NewShownQuestions returns an empty record.
func NewShownQuestions() *ShownQuestions {
	return &ShownQuestions{bySession: map[string]map[string]string{}}
}

func questionFingerprint(req api.WorkerDecisionRequest) string {
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *ShownQuestions) record(sessionID string, rows []Row) {
	shown := map[string]string{}
	for _, row := range rows {
		if row.asked != nil {
			shown[row.askedJob] = questionFingerprint(*row.asked)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bySession[strings.TrimSpace(sessionID)] = shown
}

// Take reports whether the session's latest request showed req as the
// question of jobID, and forgets it so it is answered once.
func (s *ShownQuestions) Take(sessionID, jobID string, req api.WorkerDecisionRequest) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	shown := s.bySession[strings.TrimSpace(sessionID)]
	if shown == nil || shown[jobID] != questionFingerprint(req) {
		return false
	}
	delete(shown, jobID)
	return true
}
