package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

// ProviderRejectionRule selects a notice reason from structured response data.
// CodePath is a dot-separated JSON path; array index 0 requires a singleton.
type ProviderRejectionRule struct {
	Status   int                                   `yaml:"status"`
	CodePath string                                `yaml:"code_path"`
	Code     string                                `yaml:"code"`
	Reason   providerretry.ProviderRejectionReason `yaml:"reason"`
}

func validateRejectionRules(rules []ProviderRejectionRule) error {
	for i, rule := range rules {
		if rule.Status < 400 || rule.Status >= 500 || rule.Code == "" || rule.Reason == "" || rule.CodePath == "" {
			return fmt.Errorf("rejection_reasons[%d]: status must be 4xx and code_path, code, and reason are required", i)
		}
		for _, part := range strings.Split(rule.CodePath, ".") {
			if strings.TrimSpace(part) == "" {
				return fmt.Errorf("rejection_reasons[%d]: empty code_path segment", i)
			}
		}
	}
	return nil
}

func rejectionReason(err error, rules []ProviderRejectionRule) error {
	rejected, ok := providerretry.AsProviderRequestRejected(err)
	if !ok {
		return err
	}
	if !json.Valid(rejected.ResponseBody()) {
		return err
	}
	var envelope any
	decoder := json.NewDecoder(bytes.NewReader(rejected.ResponseBody()))
	decoder.UseNumber()
	if decoder.Decode(&envelope) != nil {
		return err
	}
	for _, rule := range rules {
		if rejected.Status == rule.Status && rejectionCode(envelope, rule.CodePath) == rule.Code {
			withReason := *rejected
			withReason.Reason = rule.Reason
			return &withReason
		}
	}
	return err
}

func rejectionCode(value any, path string) string {
	for _, part := range strings.Split(path, ".") {
		switch node := value.(type) {
		case map[string]any:
			value = node[part]
		case []any:
			// Mixed error envelopes cannot establish one unambiguous cause.
			if part != "0" || len(node) != 1 {
				return ""
			}
			value = node[0]
		default:
			return ""
		}
	}
	switch code := value.(type) {
	case string:
		return code
	case json.Number:
		return code.String()
	default:
		return ""
	}
}

type rejectionReasonProvider struct {
	inner modelcall.Provider
	rules []ProviderRejectionRule
}

func (p *rejectionReasonProvider) ID() string                       { return p.inner.ID() }
func (p *rejectionReasonProvider) Models() []modelcall.ModelInfo    { return p.inner.Models() }
func (p *rejectionReasonProvider) Profile() providerprofile.Profile { return p.inner.Profile() }

func (p *rejectionReasonProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	completion, err := p.inner.Complete(ctx, req)
	return completion, rejectionReason(err, p.rules)
}

func (p *rejectionReasonProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	stream, err := p.inner.Stream(ctx, req)
	if err != nil {
		return nil, rejectionReason(err, p.rules)
	}
	out := make(chan modelcall.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range stream {
			chunk.Err = rejectionReason(chunk.Err, p.rules)
			if !modelcall.SendChunk(ctx, out, chunk) {
				return
			}
		}
	}()
	return out, nil
}
