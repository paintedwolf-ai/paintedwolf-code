package providerretry

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// modelRefusalParam names the rejected request field.
const modelRefusalParam = "model"

// DefaultModelRefusalCodes are structured refusal codes.
func DefaultModelRefusalCodes() []string {
	return []string{
		"model_not_found",
		"model_not_available",
		"invalid_model",
		"unknown_model",
	}
}

// Only request faults can refuse a model.
func modelRefusalStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// ModelRefusalEvidence says what a refusal proves about the pair, which decides
// whether it may be remembered. Refusals are sticky for the life of the process,
// so weak evidence would take a working model out of reach.
type ModelRefusalEvidence uint8

const (
	// RefusalEvidenceNone means the response did not refuse the model at all.
	RefusalEvidenceNone ModelRefusalEvidence = iota

	// RefusalEvidenceModelIdentity answers about the model name itself, so it
	// holds for later requests and may be remembered. A declared refusal code
	// carries it — model_not_found, not_found_error, NOT_FOUND — as does a typed
	// fault that names the requested resource.
	RefusalEvidenceModelIdentity

	// RefusalEvidenceInconclusive refuses this turn without proving anything
	// durable. The host separated the model from other request fields, or named
	// a resource whose absence it also reports for account state, so the turn
	// fails and nothing is recorded.
	RefusalEvidenceInconclusive
)

// Sticky reports whether the evidence justifies remembering the refusal.
func (e ModelRefusalEvidence) Sticky() bool { return e == RefusalEvidenceModelIdentity }

// ModelRefusalEvidenceFor grades a structured provider error. It reads only
// envelope fields the provider was asked to emit; the message prose is never
// consulted.
func ModelRefusalEvidenceFor(status int, parsed ParsedProviderError, declared []string) ModelRefusalEvidence {
	if !modelRefusalStatus(status) {
		return RefusalEvidenceNone
	}
	for _, token := range []string{parsed.Code, parsed.Type, parsed.Status} {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		for _, want := range declared {
			if strings.EqualFold(token, strings.TrimSpace(want)) {
				return RefusalEvidenceModelIdentity
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(parsed.Param), modelRefusalParam) {
		return RefusalEvidenceInconclusive
	}
	return RefusalEvidenceNone
}

// ErrModelRefused marks a structured model refusal.
var ErrModelRefused = errors.New("model refused by its host")

// ModelRefusedError identifies the declined provider and model.
type ModelRefusedError struct {
	ProviderID string
	Model      string
	Status     int
	// Code is the provider's structured refusal code.
	Code   string
	Detail string
	// Evidence grades what the provider's answer proved. Only an identity-level
	// refusal is remembered by ModelRefusalGate.
	Evidence ModelRefusalEvidence
}

func (e *ModelRefusedError) Error() string {
	if e == nil {
		return ErrModelRefused.Error()
	}
	if e.Detail != "" {
		return e.Detail
	}
	if e.ProviderID != "" && e.Model != "" {
		return fmt.Sprintf("provider %s does not serve model %s", e.ProviderID, e.Model)
	}
	return ErrModelRefused.Error()
}

func (e *ModelRefusedError) Is(target error) bool { return target == ErrModelRefused }

func (e *ModelRefusedError) NoticeCode() wire.NoticeCode {
	return wire.NoticeCodeModelRefused
}

// AsModelRefused extracts a structured model refusal.
func AsModelRefused(err error) (*ModelRefusedError, bool) {
	var e *ModelRefusedError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// ModelRefusalGate records refused pairs for this process. Entries only ever
// remove a specific named pair, never a whole provider.
//
// A refusal is sticky rather than a cooldown: nothing about a refused pair
// passes on its own, so an entry stands until configuration changes or restart.
type ModelRefusalGate struct {
	mu      sync.Mutex
	refused map[string]ModelRefusedError
}

// NewModelRefusalGate builds an empty gate.
func NewModelRefusalGate() *ModelRefusalGate {
	return &ModelRefusalGate{refused: make(map[string]ModelRefusedError)}
}

func modelRefusalKey(providerID, model string) string {
	return strings.TrimSpace(providerID) + "\x00" + strings.TrimSpace(model)
}

// Note records only refusals whose evidence names the model's identity. A
// refusal that merely pointed at the model field says nothing durable, and a
// sticky entry built on one would take a working model out of reach.
func (g *ModelRefusalGate) Note(providerID, model string, err error) {
	if g == nil || err == nil {
		return
	}
	refusal, ok := AsModelRefused(err)
	if !ok || refusal == nil || !refusal.Evidence.Sticky() {
		return
	}
	record := *refusal
	record.ProviderID = stringsOrDefault(record.ProviderID, providerID)
	record.Model = stringsOrDefault(record.Model, model)
	if strings.TrimSpace(record.ProviderID) == "" || strings.TrimSpace(record.Model) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refused == nil {
		g.refused = make(map[string]ModelRefusedError)
	}
	g.refused[modelRefusalKey(record.ProviderID, record.Model)] = record
}

// Refused reports a recorded refusal for one pair.
func (g *ModelRefusalGate) Refused(providerID, model string) (ModelRefusedError, bool) {
	if g == nil {
		return ModelRefusedError{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	record, ok := g.refused[modelRefusalKey(providerID, model)]
	return record, ok
}

// Err returns the standing refusal for a pair, so a caller short-circuits with
// the host's own answer rather than a summary of it.
func (g *ModelRefusalGate) Err(providerID, model string) error {
	record, ok := g.Refused(providerID, model)
	if !ok {
		return nil
	}
	return &record
}

// Clear drops any standing refusal for one pair. A completed call contradicts
// the record, so the gate heals without waiting for a configuration change.
func (g *ModelRefusalGate) Clear(providerID, model string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.refused, modelRefusalKey(providerID, model))
	g.mu.Unlock()
}

// Reset drops refusals after configuration changes.
func (g *ModelRefusalGate) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.refused = make(map[string]ModelRefusedError)
	g.mu.Unlock()
}

func stringsOrDefault(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
