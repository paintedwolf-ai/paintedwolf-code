package hitl

import (
	"context"
	"database/sql"
	"sync"

	"github.com/lycaon/lycaon/internal/authzledger"
)

// fakeAuthzRecorder satisfies AuthzRecorder for in-package tests, recording
// every append so tests can assert what was sealed.
type fakeAuthzRecorder struct {
	mu           sync.Mutex
	gateTx       []authzledger.ApprovalDecisionRecord
	capabilityTx []authzledger.CapabilityRecord
	humanTx      []authzledger.HumanGateRecord
	capability   []authzledger.CapabilityRecord
	failGateTx   error
}

func (f *fakeAuthzRecorder) AppendToolDenied(context.Context, authzledger.ToolDeniedRecord) {}

func (f *fakeAuthzRecorder) AppendCapabilityRecord(_ context.Context, rec authzledger.CapabilityRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capability = append(f.capability, rec)
	return nil
}

func (f *fakeAuthzRecorder) AppendDirectIPLifecycle(context.Context, authzledger.DirectIPLifecycleRecord) {
}

func (f *fakeAuthzRecorder) AppendMediatedEndpoint(context.Context, authzledger.MediatedEndpointRecord) {
}

func (f *fakeAuthzRecorder) AppendApprovalGateTx(_ context.Context, _ *sql.Tx, rec authzledger.ApprovalDecisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failGateTx != nil {
		return f.failGateTx
	}
	f.gateTx = append(f.gateTx, rec)
	return nil
}

func (f *fakeAuthzRecorder) AppendCapabilityGateTx(_ context.Context, _ *sql.Tx, rec authzledger.CapabilityRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capabilityTx = append(f.capabilityTx, rec)
	return nil
}

func (f *fakeAuthzRecorder) AppendHumanGateTx(_ context.Context, _ *sql.Tx, rec authzledger.HumanGateRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.humanTx = append(f.humanTx, rec)
	return nil
}
