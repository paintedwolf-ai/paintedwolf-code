package spendguard

import (
	"github.com/lycaon/lycaon/internal/noticeerr"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ErrCeiling is returned when a session hits its opt-in USD spend ceiling.
var ErrCeiling error = noticeerr.NewSentinel("session_spend_ceiling", wire.NoticeCodeSessionSpendCeilingReached)

// CeilingReached carries ceiling and spent amounts for notice rendering.
type CeilingReached struct {
	CeilingUSD float64
	SpentUSD   float64
	// Coverage qualifies SpentUSD; the counters below are its causes.
	Coverage            wire.CostEstimateCoverage
	UnpricedTokens      int
	UnknownChargedCalls int
}

func (e *CeilingReached) Error() string {
	return ErrCeiling.Error()
}

func (e *CeilingReached) Unwrap() error {
	return ErrCeiling
}

// NoticeCeilingUSD exposes the ceiling for user-notice template vars.
func (e *CeilingReached) NoticeCeilingUSD() float64 {
	if e == nil {
		return 0
	}
	return e.CeilingUSD
}

// NoticeSpentUSD exposes spent amount for user-notice template vars.
func (e *CeilingReached) NoticeSpentUSD() float64 {
	if e == nil {
		return 0
	}
	return e.SpentUSD
}

// NoticeEstimateCoverage exposes the host classification of the spent amount.
func (e *CeilingReached) NoticeEstimateCoverage() wire.CostEstimateCoverage {
	if e == nil {
		return ""
	}
	return e.Coverage
}

// NoticeUnpricedTokens exposes usage without a price.
func (e *CeilingReached) NoticeUnpricedTokens() int {
	if e == nil {
		return 0
	}
	return e.UnpricedTokens
}

// NoticeUnknownChargedCalls exposes provider calls with missing usage.
func (e *CeilingReached) NoticeUnknownChargedCalls() int {
	if e == nil {
		return 0
	}
	return e.UnknownChargedCalls
}
