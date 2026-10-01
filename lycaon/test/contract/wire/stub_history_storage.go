package contract

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubHistoryPlan struct {
	request    api.HistoryRetentionRequest
	projectID  string
	candidates []api.HistoryPruneCandidate
	created    time.Time
}

type stubHistoryBody struct {
	api.HistoryPruneCandidate
	SessionID string
}

type stubHistoryStorage struct {
	mu          sync.Mutex
	policy      api.HistoryRetentionPolicy
	bodies      []stubHistoryBody
	protections []api.HistoryProtection
	plans       map[string]stubHistoryPlan
	writeJSON   stubJSONWriter
}

func registerStubHistoryStorageRoutes(mux *http.ServeMux, writeJSON stubJSONWriter) {
	s := &stubHistoryStorage{policy: historyretention.DefaultPolicy(), protections: []api.HistoryProtection{}, plans: make(map[string]stubHistoryPlan), writeJSON: writeJSON}
	for _, class := range []string{"recordings", "checkpoints", "source_revisions", "scan_detail", "receipt_detail"} {
		for index := range 2 {
			s.bodies = append(s.bodies, stubHistoryBody{HistoryPruneCandidate: api.HistoryPruneCandidate{ID: fmt.Sprintf("%s-%d", class, index), Class: class, ProjectID: fixtureProjectID, CreatedAt: fixtureTimeValue(), ReclaimableBytes: 1 << 20}, SessionID: fixtureSessionID})
		}
	}
	mux.HandleFunc("GET /v1/history-storage", s.status)
	mux.HandleFunc("PATCH /v1/history-storage", s.save)
	mux.HandleFunc("POST /v1/history-storage/preview", s.preview)
	mux.HandleFunc("POST /v1/history-storage/prune", s.prune)
	mux.HandleFunc("POST /v1/history-storage/protections", s.createProtection)
	mux.HandleFunc("DELETE /v1/history-storage/protections/{protection_id}", s.deleteProtection)
}

func (s *stubHistoryStorage) reject(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, api.ErrorResponse{Code: api.ApiErrorCodeInvalidRequest, Message: message})
}

func stubHistoryRules(policy api.HistoryRetentionPolicy) map[string]api.HistoryRetentionRule {
	return map[string]api.HistoryRetentionRule{"recordings": policy.Recordings, "checkpoints": policy.Checkpoints, "source_revisions": policy.SourceRevisions, "scan_detail": policy.ScanDetail, "receipt_detail": policy.ReceiptDetail}
}

func (s *stubHistoryStorage) request(w http.ResponseWriter, r *http.Request) (api.HistoryRetentionRequest, bool) {
	var request api.HistoryRetentionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.reject(w, http.StatusBadRequest, "Invalid retention request.")
		return request, false
	}
	valid := request.Policy.Version == 1 && request.Policy.Revision > 0
	for _, rule := range stubHistoryRules(request.Policy) {
		switch rule.Mode {
		case "forever":
			valid = valid && rule.MaxAgeDays == 0 && rule.MaxBytes == 0
		case "max_age":
			valid = valid && rule.MaxAgeDays > 0 && rule.MaxAgeDays <= 365000 && rule.MaxBytes == 0
		case "max_bytes":
			valid = valid && rule.MaxBytes > 0 && rule.MaxAgeDays == 0
		default:
			valid = false
		}
	}
	if !valid {
		s.reject(w, http.StatusBadRequest, "Invalid retention policy.")
		return request, false
	}
	if request.Policy.Revision != s.policy.Revision {
		s.reject(w, http.StatusConflict, "History changed; preview again.")
		return request, false
	}
	return request, true
}

func (s *stubHistoryStorage) status(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lanes := []api.HistoryStorageLane{}
	for _, class := range []string{"recordings", "checkpoints", "source_revisions", "scan_detail", "receipt_detail"} {
		lane := api.HistoryStorageLane{ID: class}
		for _, body := range s.bodies {
			if body.Class == class {
				lane.StoredBytes += body.ReclaimableBytes
			}
		}
		lane.LogicalBytes = lane.StoredBytes
		lanes = append(lanes, lane)
	}
	s.writeJSON(w, http.StatusOK, api.HistoryStorageStatus{Policy: s.policy, Lanes: lanes, Protections: slices.Clone(s.protections)})
}

func (s *stubHistoryStorage) eligible(request api.HistoryRetentionRequest, projectID string) []api.HistoryPruneCandidate {
	candidates := []api.HistoryPruneCandidate{}
	if projectID != "" && projectID != fixtureProjectID {
		return candidates
	}
	remaining := make(map[string]int64)
	for _, body := range s.bodies {
		remaining[body.Class] += body.ReclaimableBytes
	}
	rules := stubHistoryRules(request.Policy)
	for _, body := range s.bodies {
		if slices.ContainsFunc(s.protections, func(protection api.HistoryProtection) bool {
			return protection.Protected && ((protection.ScopeType == "project" && protection.ScopeID == body.ProjectID) || (protection.ScopeType == "session" && protection.ScopeID == body.SessionID))
		}) {
			continue
		}
		rule := rules[body.Class]
		before := time.Now().AddDate(0, 0, -int(rule.MaxAgeDays))
		if (rule.Mode == "max_age" && fixtureTimeValue().Before(before)) || (rule.Mode == "max_bytes" && remaining[body.Class] > rule.MaxBytes) {
			candidates = append(candidates, body.HistoryPruneCandidate)
			remaining[body.Class] -= body.ReclaimableBytes
		}
	}
	return candidates
}

func (s *stubHistoryStorage) remember(request api.HistoryRetentionRequest, projectID string, candidates []api.HistoryPruneCandidate) api.HistoryRetentionPreview {
	token := uuid.NewString()
	request.PreviewToken = ""
	s.plans[token] = stubHistoryPlan{request: request, projectID: projectID, candidates: candidates, created: time.Now()}
	preview := api.HistoryRetentionPreview{Token: token, PolicyRevision: s.policy.Revision, Candidates: candidates, EligibleCount: int64(len(candidates)), Complete: len(candidates) <= 1}
	for _, candidate := range candidates {
		preview.ReclaimableBytes += candidate.ReclaimableBytes
	}
	return preview
}

func (s *stubHistoryStorage) preview(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.request(w, r)
	if !ok {
		return
	}
	projectID := r.URL.Query().Get("project_id")
	s.writeJSON(w, http.StatusOK, s.remember(request, projectID, s.eligible(request, projectID)))
}

func (s *stubHistoryStorage) reviewed(w http.ResponseWriter, r *http.Request) (api.HistoryRetentionRequest, stubHistoryPlan, bool) {
	request, ok := s.request(w, r)
	if !ok {
		return request, stubHistoryPlan{}, false
	}
	plan, found := s.plans[request.PreviewToken]
	projectID := r.URL.Query().Get("project_id")
	if !found || time.Since(plan.created) >= 10*time.Minute || plan.request.Policy != request.Policy || plan.projectID != projectID {
		s.reject(w, http.StatusConflict, "History changed; preview again.")
		return request, plan, false
	}
	return request, plan, true
}

func (s *stubHistoryStorage) save(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, _, ok := s.reviewed(w, r)
	if !ok {
		return
	}
	s.policy = request.Policy
	s.policy.Revision++
	s.policy.Suspended = false
	clear(s.plans)
	s.writeJSON(w, http.StatusOK, s.policy)
}

func (s *stubHistoryStorage) prune(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, plan, ok := s.reviewed(w, r)
	if !ok {
		return
	}
	projectID := r.URL.Query().Get("project_id")
	result := api.HistoryPruneResult{}
	if len(plan.candidates) > 0 {
		candidate := plan.candidates[0]
		s.bodies = slices.DeleteFunc(s.bodies, func(body stubHistoryBody) bool { return body.ID == candidate.ID })
		result.RemovedCount = 1
		result.ReleasedBytes = candidate.ReclaimableBytes
		plan.candidates = plan.candidates[1:]
	}
	clear(s.plans)
	result.Complete = len(plan.candidates) == 0
	result.PreviewToken = s.remember(request, projectID, plan.candidates).Token
	s.writeJSON(w, http.StatusOK, result)
}

func (s *stubHistoryStorage) createProtection(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var protection api.HistoryProtection
	if err := json.NewDecoder(r.Body).Decode(&protection); err != nil ||
		!((protection.ScopeType == "project" && protection.ScopeID == fixtureProjectID) || (protection.ScopeType == "session" && protection.ScopeID == fixtureSessionID)) {
		s.reject(w, http.StatusBadRequest, "History protection owner not found.")
		return
	}
	s.protections = slices.DeleteFunc(s.protections, func(value api.HistoryProtection) bool {
		return value.ScopeType == protection.ScopeType && value.ScopeID == protection.ScopeID
	})
	if protection.Protected {
		s.protections = append(s.protections, protection)
	}
	clear(s.plans)
	s.writeJSON(w, http.StatusCreated, protection)
}

func (s *stubHistoryStorage) deleteProtection(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	protectionID := r.PathValue("protection_id")
	found := false
	s.protections = slices.DeleteFunc(s.protections, func(value api.HistoryProtection) bool {
		if value.ScopeID == protectionID {
			found = true
			return true
		}
		return false
	})
	if !found && protectionID != fixtureCheckpointID {
		s.writeJSON(w, http.StatusNotFound, api.ErrorResponse{Code: api.ApiErrorCodeHistoryProtectionNotFound, Message: "History protection not found."})
		return
	}
	clear(s.plans)
	w.WriteHeader(http.StatusNoContent)
}
