package api

import (
	"net/http"
	"os"
	"strconv"
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
	"golang.org/x/time/rate"
)

type rateLimitState struct {
	sessions *rate.Limiter
	prompts  *rate.Limiter
	// writes caps mutating routes that lack narrower budgets.
	writes   *rate.Limiter
	source   *rate.Limiter
	editor   *rate.Limiter
	presence *rate.Limiter
}

func newLimiter(perMinute int) *rate.Limiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	// A full burst allows perMinute immediate requests.
	return rate.NewLimiter(rate.Limit(float64(perMinute)/60.0), perMinute)
}

func newRateLimitState() *rateLimitState {
	sessPerMin := envInt("LYCAON_RATE_SESSIONS_PER_MIN", 10)
	promptPerMin := envInt("LYCAON_RATE_PROMPTS_PER_MIN", 60)
	writesPerMin := envInt("LYCAON_RATE_WRITES_PER_MIN", 600)
	return &rateLimitState{
		sessions: newLimiter(sessPerMin),
		prompts:  newLimiter(promptPerMin),
		writes:   newLimiter(writesPerMin),
		source:   newLimiter(envInt("LYCAON_RATE_SOURCE_PER_MIN", 1200)),
		editor:   newLimiter(envInt("LYCAON_RATE_EDITOR_PER_MIN", 1200)),
		presence: newLimiter(envInt("LYCAON_RATE_PRESENCE_PER_MIN", 600)),
	}
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// Rate admission uses registered operation identities, independently of authorization.
func (s *Server) rateLimitOperation(operation generatedOperation, next http.Handler) http.Handler {
	limiter := s.operationLimiter(operation)
	if limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			s.writeRateLimited(w, limiter, operation.ID+" rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) operationLimiter(operation generatedOperation) *rate.Limiter {
	switch operation.ID {
	case operationCreateSession.ID:
		return s.rateLimits.sessions
	case operationSendPrompt.ID:
		return s.rateLimits.prompts
	case operationReleaseSourceView.ID, operationReleaseSourcePresentation.ID,
		operationReleaseSourceViewportInterest.ID, operationLeaveEditorDocument.ID,
		operationWithdrawProjectSourcePresentation.ID:
		// Releasing owned resources must remain possible while admission is busy.
		return nil
	case operationCreateSourceView.ID, operationApplySourceViewIntent.ID, operationDigestSourceComparisons.ID,
		operationCreateSourcePresentation.ID, operationReplaceSourceViewportInterest.ID,
		operationCompleteProjectSourcePresentation.ID:
		return s.rateLimits.source
	case operationOpenEditorDocument.ID, operationObserveEditorDocument.ID,
		operationSyncEditorDocument.ID, operationSubmitEditorDocumentUpdate.ID,
		operationReplaceEditorDocument.ID, operationReplaceEditorDocumentRetention.ID,
		operationReadEditorDocumentStatuses.ID:
		return s.rateLimits.editor
	case operationPublishEditorDocumentPresence.ID:
		return s.rateLimits.presence
	}
	switch operation.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	default:
		return s.rateLimits.writes
	}
}

func (s *Server) writeRateLimited(w http.ResponseWriter, limiter *rate.Limiter, debugMessage string) {
	now := time.Now()
	reservation := limiter.ReserveN(now, 1)
	delay := reservation.DelayFrom(now)
	reservation.CancelAt(now)
	seconds := max(1, int((delay+time.Second-1)/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	s.responses.Fail(w, wire.ApiErrorCodeRateLimited, debugMessage)
}
