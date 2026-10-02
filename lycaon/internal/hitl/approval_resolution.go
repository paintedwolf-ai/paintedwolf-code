package hitl

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApprovalAuthorityInstaller applies one approval option.
type ApprovalAuthorityInstaller interface {
	InstallApprovalOption(ctx context.Context, checkpointID string, option ApprovalOption) (rollback func(), err error)
}

type approvalAuthorityRecovery interface {
	RollbackApprovalOption(ctx context.Context, checkpointID string, option ApprovalOption) error
}

// SetApprovalAuthorityInstaller wires the one authority mutation boundary.
func (m *Manager) SetApprovalAuthorityInstaller(installer ApprovalAuthorityInstaller) {
	if m != nil {
		m.authorityInstaller = installer
	}
}

// ApprovalResolver says who settles an approval: the deciding person, or a
// named standing policy rule. Its constructors are the whole vocabulary.
type ApprovalResolver struct {
	policy *authzledger.PolicyIdentity
	// proof is the deciding person's signed presence, required to release
	// values they hold.
	proof *presence.Proof
}

// HumanApproval resolves as the deciding person.
func HumanApproval() ApprovalResolver { return ApprovalResolver{} }

// AttestedApproval resolves as the deciding person with their verified
// presence, which a plan releasing held values requires.
func AttestedApproval(proof presence.Proof) ApprovalResolver { return ApprovalResolver{proof: &proof} }

// PolicyApproval resolves under the named standing policy rule, with no person.
func PolicyApproval(policy authzledger.PolicyIdentity) ApprovalResolver {
	return ApprovalResolver{policy: &policy}
}

func (r ApprovalResolver) validate() error {
	if r.policy != nil && !r.policy.Complete() {
		return fmt.Errorf("%w: a policy resolution names its pack, unit, and rule", ErrApprovalResolverInvalid)
	}
	if r.policy != nil && r.proof != nil {
		return fmt.Errorf("%w: a policy resolution carries no presence", ErrApprovalResolverInvalid)
	}
	return nil
}

// approvalResolution names who settles the checkpoint; a person resolution needs the deciding person.
func approvalResolution(ctx context.Context, resolver ApprovalResolver) (Resolution, error) {
	if resolver.policy != nil {
		return policyResolution(*resolver.policy), nil
	}
	return personResolution(ctx)
}

// grantedBy stamps each grant the option installs with its approver, so the
// grant names them after the checkpoint's chat is gone.
func (o ApprovalOption) grantedBy(resolution Resolution) ApprovalOption {
	authority := make([]ApprovalAuthorityDelta, len(o.Authority))
	for i, delta := range o.Authority {
		if delta.Grant != nil {
			grant := *delta.Grant
			grant.GrantedByPersonID = resolution.PersonID
			grant.GrantedByPolicy = resolution.Policy
			delta.Grant = &grant
		}
		authority[i] = delta
	}
	o.Authority = authority
	return o
}


// ResolveApprovalOption installs authority before committing the checkpoint.
// A failed commit rolls back the installed authority.
func (m *Manager) ResolveApprovalOption(ctx context.Context, sessionID, checkpointID, optionID string) (*CheckpointResponse, error) {
	return m.ResolveApprovalOptionBy(ctx, sessionID, checkpointID, optionID, HumanApproval())
}

// ResolveApprovalOptionBy installs authority before committing the checkpoint as the given resolver.
func (m *Manager) ResolveApprovalOptionBy(ctx context.Context, sessionID, checkpointID, optionID string, resolver ApprovalResolver) (*CheckpointResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := resolver.validate(); err != nil {
		return nil, err
	}
	if m.sessionAdmission != nil {
		var response *CheckpointResponse
		err := m.sessionAdmission(ctx, sessionID, func() error {
			var resolveErr error
			response, resolveErr = m.resolveApprovalOptionAdmitted(ctx, sessionID, checkpointID, optionID, resolver)
			return resolveErr
		})
		return response, err
	}
	return m.resolveApprovalOptionAdmitted(ctx, sessionID, checkpointID, optionID, resolver)
}

func (m *Manager) resolveApprovalOptionAdmitted(ctx context.Context, sessionID, checkpointID, optionID string, resolver ApprovalResolver) (*CheckpointResponse, error) {
	releaseAuthority := m.LockApprovalAuthority()
	defer releaseAuthority()
	unlock := m.resolutionLocks.Lock(checkpointID)
	defer unlock()

	row, err := m.store.Get(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if row.SessionID != sessionID {
		return nil, ErrCheckpointNotFound
	}
	if row.Kind != api.CheckpointKindToolApproval {
		return nil, ErrCheckpointKindMismatch
	}
	if row.Status != DecisionStatusPending {
		if row.Status == DecisionStatusApproved && row.Result != nil && row.Result.OptionID == optionID {
			return storedToCheckpointResponse(row), nil
		}
		return nil, ErrCheckpointNotPending
	}
	raw, _ := row.Payload["approval_plan"].(map[string]any)
	plan, err := approvalPlanFromMap(raw)
	if err != nil {
		return nil, fmt.Errorf("approval plan: %w", err)
	}
	option, ok := plan.Option(optionID)
	if !ok {
		return nil, ErrApprovalOptionNotFound
	}
	if option.Disabled {
		return nil, ErrApprovalOptionUnavailable
	}
	if m.authorityInstaller == nil {
		return nil, fmt.Errorf("approval authority installer is not configured")
	}
	resolution, err := approvalResolution(ctx, resolver)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var release *verifiedRelease
	if plan.releasesHeld(option) {
		verified, err := m.verifyRelease(sessionID, checkpointID, *plan, option, resolver, resolution)
		if err != nil {
			return nil, err
		}
		release = &verified
		if option, err = attestHeldGrants(option, plan.Held, verified.attestation, m.held.ledger, now); err != nil {
			m.forgetAttested(option)
			return nil, err
		}
	}
	option = option.grantedBy(resolution)
	if err := m.store.prepareApprovalOperation(ctx, checkpointID, sessionID, option); err != nil {
		m.forgetAttested(option)
		return nil, fmt.Errorf("prepare approval authority: %w", err)
	}

	result := &DecisionResult{Approved: plan.OptionContinues(option), OptionID: option.ID}
	if option.DecisionAction == ApprovalOptionRedact {
		result.RedactSecrets = true
	}
	if option.DecisionAction == ApprovalOptionTrack {
		result.TrackSecrets = true
	}
	if release != nil {
		result.AttestationID = release.attestation.ID
	}
	seenGrantIDs := map[string]struct{}{}
	for _, delta := range option.Authority {
		id := ""
		if delta.Grant != nil {
			id = delta.Grant.ID
		} else if delta.AskQuiet != nil {
			id = delta.AskQuiet.ID
		}
		if id == "" {
			continue
		}
		if _, seen := seenGrantIDs[id]; !seen {
			result.GrantIDs = append(result.GrantIDs, id)
			seenGrantIDs[id] = struct{}{}
		}
		if result.GrantScope == "" {
			result.GrantScope = option.Scope
			result.GrantTitle = option.Title
		}
	}
	sealed := *row
	sealed.Status = DecisionStatusApproved
	sealed.Result = result
	sealed.ResolvedAt = &now
	sealed.Resolution = &resolution
	seal := func(tx *sql.Tx) error {
		if err := m.authzRecorder.AppendApprovalGateTx(ctx, tx, approvalRecordInput(sealed, DecisionStatusApproved)); err != nil {
			return err
		}
		if err := recordChatGrantsTx(ctx, tx, checkpointID, option, now); err != nil {
			return err
		}
		if release != nil {
			if err := m.held.recorder.RecordReleaseTx(ctx, tx, AttestedRelease{
				Attestation: release.attestation, WindowLabel: release.windowLabel, CheckpointID: checkpointID,
				Scope: releaseScope(option), Held: *plan.Held, Recipients: plan.Held.Recipients,
			}); err != nil {
				return err
			}
		}
		return m.sealDetectionResolvedTx(ctx, tx, sealed, DecisionStatusApproved)
	}
	rollbackAuthority, err := m.authorityInstaller.InstallApprovalOption(ctx, checkpointID, option)
	if err != nil {
		_ = m.store.rollbackApprovalOperation(context.WithoutCancel(ctx), checkpointID)
		m.forgetAttested(option)
		return nil, fmt.Errorf("install approved authority: %w", err)
	}
	viaOutbox, err := m.store.commitApprovalOperation(ctx, *row, result, now, resolution, seal)
	if err != nil {
		if rollbackAuthority != nil {
			rollbackAuthority()
		}
		_ = m.store.rollbackApprovalOperation(context.WithoutCancel(ctx), checkpointID)
		m.forgetAttested(option)
		return nil, err
	}
	row.Status = DecisionStatusApproved
	row.Result = result
	row.ResolvedAt = &now
	row.Resolution = &resolution
	m.announceResolved(ctx, *row, viaOutbox)
	m.notifyToolApprovalTerminal(*row, DecisionStatusApproved)
	return storedToCheckpointResponse(row), nil
}

// RecoverApprovalOperations rolls back authority installed without a committed checkpoint.
func (m *Manager) RecoverApprovalOperations(ctx context.Context) error {
	pending, err := m.store.preparedApprovalOperations(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	recovery, ok := m.authorityInstaller.(approvalAuthorityRecovery)
	if !ok {
		return fmt.Errorf("approval authority recovery is not configured")
	}
	for _, op := range pending {
		if err := recovery.RollbackApprovalOption(ctx, op.CheckpointID, op.Option); err != nil {
			return err
		}
		if err := m.store.rollbackApprovalOperation(ctx, op.CheckpointID); err != nil {
			return err
		}
	}
	return nil
}
