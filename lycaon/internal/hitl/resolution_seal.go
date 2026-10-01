package hitl

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

// keyedMutex retains each mutex until its holders and waiters have released it.
type keyedMutex struct {
	mu      sync.Mutex
	entries map[string]*keyedMutexEntry
}

type keyedMutexEntry struct {
	refs int
	mu   sync.Mutex
}

// Lock blocks until the key is held and returns the matching unlock.
func (k *keyedMutex) Lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.entries == nil {
		k.entries = map[string]*keyedMutexEntry{}
	}
	e := k.entries[key]
	if e == nil {
		e = &keyedMutexEntry{}
		k.entries[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.entries, key)
		}
		k.mu.Unlock()
	}
}

// resolutionSeal records the outcome in the checkpoint's resolving transaction.
func (m *Manager) resolutionSeal(ctx context.Context, status DecisionStatus) func(*sql.Tx, StoredCheckpoint) error {
	return func(tx *sql.Tx, committed StoredCheckpoint) error {
		switch committed.Kind {
		case api.CheckpointKindToolApproval:
			if err := m.authzRecorder.AppendApprovalGateTx(ctx, tx, approvalRecordInput(committed, status)); err != nil {
				return err
			}
			return m.sealDetectionResolvedTx(ctx, tx, committed, status)
		case api.CheckpointKindContentApply:
			decision := ""
			if committed.ContentResult != nil {
				decision = string(committed.ContentResult.Decision)
			}
			return m.authzRecorder.AppendHumanGateTx(ctx, tx, authzledger.HumanGateRecord{
				SessionID:        committed.SessionID,
				Action:           authzledger.ActionContentApplyResolved,
				Outcome:          gateOutcome(status),
				ResolvedBy:       resolutionBy(committed.Resolution),
				ResolverPersonID: resolutionPersonID(committed.Resolution),
				Tool:             committed.ToolName,
				Files:            []string{committed.Path},
				ProjectDir:       committed.ProjectDir,
				ContentDecision:  decision,
			})
		default:
			return nil
		}
	}
}

// personResolution names the person whose request is answering a checkpoint.
func personResolution(ctx context.Context) (Resolution, error) {
	person, err := people.Deciding(ctx)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve checkpoint person: %w", err)
	}
	return Resolution{By: authzledger.ResolvedByHuman, PersonID: person.ID}, nil
}

// stopResolution credits a stop to the person whose request stopped the
// session. A stop no person requested, such as an agent leaving its workflow,
// is the host's.
func stopResolution(ctx context.Context) Resolution {
	if person, err := people.Deciding(ctx); err == nil {
		return Resolution{By: authzledger.ResolvedByUserStop, PersonID: person.ID}
	}
	return Resolution{By: authzledger.ResolvedByHostStop}
}

func policyResolution(policy authzledger.PolicyIdentity) Resolution {
	return Resolution{By: authzledger.ResolvedByPolicy, Policy: &policy}
}

func gateOutcome(status DecisionStatus) string {
	if status == DecisionStatusApproved {
		return authzledger.OutcomeAllowed
	}
	return authzledger.OutcomeDenied
}

// storedDetection retains the Sigma citation used when sealing a restored checkpoint.
type storedDetection struct {
	PackID               string                    `json:"pack_id"`
	RuleID               string                    `json:"rule_id"`
	RuleTitle            string                    `json:"rule_title,omitempty"`
	Level                string                    `json:"level,omitempty"`
	ActionID             string                    `json:"action_id,omitempty"`
	Direct               bool                      `json:"direct,omitempty"`
	DeclaredDestinations []string                  `json:"declared_destinations,omitempty"`
	Endpoints            []storedDetectionEndpoint `json:"endpoints,omitempty"`
	Sockets              []storedDetectionSocket   `json:"sockets,omitempty"`
}

type storedDetectionEndpoint struct {
	Host      string `json:"host"`
	Port      uint16 `json:"port,omitempty"`
	Transport string `json:"transport,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
}

type storedDetectionSocket struct {
	ApprovedPath string `json:"approved_path"`
	ResolvedPath string `json:"resolved_path,omitempty"`
}

const payloadKeyDetection = "detection"

// seedPayloadDetection persists the winning detection citation with the
// pending checkpoint.
func seedPayloadDetection(row *StoredCheckpoint, req CheckpointRequest) {
	if req.Detection == nil || req.ProposedAction == nil {
		return
	}
	det := storedDetection{
		PackID:               req.Detection.PackID,
		RuleID:               req.Detection.RuleID,
		RuleTitle:            req.Detection.RuleTitle,
		Level:                req.Detection.Level,
		ActionID:             strings.TrimSpace(req.ProposedAction.ActionID),
		Direct:               req.ProposedAction.DirectIPRequested,
		DeclaredDestinations: append([]string(nil), req.ProposedAction.DeclaredDestinations...),
	}
	if det.ActionID == "" {
		det.ActionID = strings.TrimSpace(req.ToolCallID)
	}
	for _, ep := range req.DetectionEndpoints {
		det.Endpoints = append(det.Endpoints, storedDetectionEndpoint{
			Host: ep.Host, Port: ep.Port, Transport: ep.Transport, Attempts: ep.Attempts,
		})
	}
	for _, grant := range req.ProposedAction.SocketGrants {
		det.Sockets = append(det.Sockets, storedDetectionSocket{
			ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath,
		})
	}
	raw, err := json.Marshal(det)
	if err != nil {
		return
	}
	stored := map[string]any{}
	if json.Unmarshal(raw, &stored) == nil {
		row.Payload[payloadKeyDetection] = stored
	}
}

func detectionFromPayload(row StoredCheckpoint) (storedDetection, bool) {
	raw, ok := row.Payload[payloadKeyDetection].(map[string]any)
	if !ok || raw == nil {
		return storedDetection{}, false
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return storedDetection{}, false
	}
	var det storedDetection
	if json.Unmarshal(buf, &det) != nil {
		return storedDetection{}, false
	}
	if strings.TrimSpace(det.RuleID) == "" && strings.TrimSpace(det.PackID) == "" {
		return storedDetection{}, false
	}
	return det, true
}

// sealDetectionResolvedTx appends detection_resolved from the citation stored
// with the checkpoint, in the same transaction as the decision.
func (m *Manager) sealDetectionResolvedTx(ctx context.Context, tx *sql.Tx, committed StoredCheckpoint, status DecisionStatus) error {
	det, ok := detectionFromPayload(committed)
	if !ok {
		return nil
	}
	outcome := gateOutcome(status)
	endpoints := make([]authzledger.CapabilityEndpoint, 0, len(det.Endpoints))
	for _, ep := range det.Endpoints {
		endpoints = append(endpoints, authzledger.CapabilityEndpoint{
			Host: ep.Host, Port: ep.Port, Transport: ep.Transport,
			Allowed: outcome == authzledger.OutcomeAllowed, Attempts: ep.Attempts,
		})
	}
	sockets := make([]authzledger.CapabilitySocket, 0, len(det.Sockets))
	for _, s := range det.Sockets {
		sockets = append(sockets, authzledger.CapabilitySocket{
			ApprovedPath: s.ApprovedPath, ResolvedPath: s.ResolvedPath, Scope: authzledger.GrantScopeOnce,
		})
	}
	return m.authzRecorder.AppendCapabilityGateTx(ctx, tx, authzledger.CapabilityRecord{
		SessionID:            committed.SessionID,
		ToolCallID:           checkpointToolCallID(committed),
		Action:               authzledger.ActionDetectionResolved,
		Outcome:              outcome,
		ResolvedBy:           resolutionBy(committed.Resolution),
		ResolverPersonID:     resolutionPersonID(committed.Resolution),
		Tool:                 committed.ToolName,
		Endpoints:            endpoints,
		Sockets:              sockets,
		DeclaredDestinations: append([]string(nil), det.DeclaredDestinations...),
		Direct:               det.Direct,
		Detections: []authzledger.CapabilityDetection{{
			PackID:    det.PackID,
			RuleID:    det.RuleID,
			RuleTitle: det.RuleTitle,
			Level:     det.Level,
			ActionID:  det.ActionID,
		}},
	})
}
