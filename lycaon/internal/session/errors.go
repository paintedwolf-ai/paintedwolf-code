package session

import (
	"strings"

	"github.com/lycaon/lycaon/internal/noticeerr"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ErrSessionSpendCeiling is returned when a session hits its opt-in USD spend ceiling.
var ErrSessionSpendCeiling error = noticeerr.NewSentinel("session_spend_ceiling", wire.NoticeCodeSessionSpendCeilingReached)

// SessionSpendCeilingReached carries ceiling and spent amounts for notice rendering.
type SessionSpendCeilingReached struct {
	CeilingUSD float64
	SpentUSD   float64
	// Coverage qualifies SpentUSD; the counters below are its causes.
	Coverage            wire.CostEstimateCoverage
	UnpricedTokens      int
	UnknownChargedCalls int
}

func (e *SessionSpendCeilingReached) Error() string {
	return ErrSessionSpendCeiling.Error()
}

func (e *SessionSpendCeilingReached) Unwrap() error {
	return ErrSessionSpendCeiling
}

// NoticeCeilingUSD exposes the ceiling for user-notice template vars.
func (e *SessionSpendCeilingReached) NoticeCeilingUSD() float64 {
	if e == nil {
		return 0
	}
	return e.CeilingUSD
}

// NoticeSpentUSD exposes spent amount for user-notice template vars.
func (e *SessionSpendCeilingReached) NoticeSpentUSD() float64 {
	if e == nil {
		return 0
	}
	return e.SpentUSD
}

// NoticeEstimateCoverage exposes the host classification of the spent amount.
func (e *SessionSpendCeilingReached) NoticeEstimateCoverage() wire.CostEstimateCoverage {
	if e == nil {
		return ""
	}
	return e.Coverage
}

// NoticeUnpricedTokens exposes usage without a price.
func (e *SessionSpendCeilingReached) NoticeUnpricedTokens() int {
	if e == nil {
		return 0
	}
	return e.UnpricedTokens
}

// NoticeUnknownChargedCalls exposes provider calls with missing usage.
func (e *SessionSpendCeilingReached) NoticeUnknownChargedCalls() int {
	if e == nil {
		return 0
	}
	return e.UnknownChargedCalls
}

// ErrGroundingEscalated is returned when circuit breaker blocks coordinator prompts.
var ErrGroundingEscalated error = noticeerr.NewSentinel("grounding_escalated", wire.NoticeCodeGroundingEscalated)

// ErrGroundingNudge is a non-fatal grounding warning injected after a turn.
// Code is the first fired identifier ([OAR-EVAL-19]). Items is every advisory.
type ErrGroundingNudge struct {
	Code  string
	Data  map[string]any
	Copy  map[string]string
	Items []GuidanceNudge
}

func (e *ErrGroundingNudge) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// Nudges returns Items, or Code when Items is empty.
func (e *ErrGroundingNudge) Nudges() []GuidanceNudge {
	if e == nil {
		return nil
	}
	if len(e.Items) > 0 {
		return e.Items
	}
	if strings.TrimSpace(e.Code) == "" {
		return nil
	}
	return []GuidanceNudge{{Code: e.Code, Data: e.Data, Copy: e.Copy}}
}
