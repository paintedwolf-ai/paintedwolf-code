package toolexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// ValidatedProgramIdentity is supplied by the sealed parsed program.
type ValidatedProgramIdentity interface {
	ValidatedProgramIdentity(context.Context) (sourceDigest, catalogDigest string, err error)
}

// ProgramProgressUpdate is a distinct host invocation of update_progress inside a program envelope.
type ProgramProgressUpdate struct {
	ExecutionID string
	Arguments   map[string]any
}

// PreparedProgramEnvelope owns admitted immutable envelope facts and progress preconditions.
type PreparedProgramEnvelope struct {
	program        ValidatedProgramIdentity
	sourceDigest   string
	catalogDigest  string
	contractDigest string
	frame          ProgramFrame
	update         *nativeArguments
	basis          progress.PreparedProgressBasis
	admission      *toolcontract.ProgramMemory
	identity       toolcontract.ProgramProgressIdentity
	closed         bool
	attempted      bool
	releaseFrame   func()
}

// ProgramProgressResult records progress independently of later program operations.
type ProgramProgressResult struct {
	Identity     toolcontract.ProgramProgressIdentity
	OwnerInvoked bool
	Outcome      progress.MutationResult
}

// PrepareProgramEnvelope validates syntax and progress arguments and snapshots host preconditions.
func (e *Executor) PrepareProgramEnvelope(
	ctx context.Context,
	frame ProgramFrame,
	program ValidatedProgramIdentity,
	update *ProgramProgressUpdate,
	store progress.Store,
	facts ProgramProgressFacts,
	admission *toolcontract.ProgramMemory,
	tc tools.ToolContext,
) (*PreparedProgramEnvelope, error) {
	end, err := frame.beginUse()
	if err != nil {
		return nil, err
	}
	held := false
	defer func() {
		if !held {
			end()
		}
	}()
	if e == nil || program == nil {
		return nil, fmt.Errorf("sealed program and executor required")
	}
	source, catalog, err := program.ValidatedProgramIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if len(source) != 64 || catalog != frame.lifetime.facts.parent.CatalogDigest {
		return nil, fmt.Errorf("validated program identity changed")
	}
	prepared := &PreparedProgramEnvelope{
		program:      program,
		sourceDigest: source,
		catalogDigest: catalog,
		frame:        frame,
		releaseFrame: end,
	}
	if update == nil {
		held = true
		return prepared, nil
	}
	definition, ok := e.definitionFor("update_progress")
	if !ok || definition.Contract.Owner == "" {
		return nil, fmt.Errorf("progress owner unavailable")
	}
	if strings.TrimSpace(update.ExecutionID) == "" ||
		strings.TrimSpace(update.ExecutionID) != update.ExecutionID ||
		strings.ContainsAny(update.ExecutionID, "\x00\x01") ||
		update.ExecutionID == frame.lifetime.facts.parent.Identity.ExecutionID {
		return nil, fmt.Errorf("separate host progress execution identity required")
	}
	if err := frame.validateHostContext(ctx, tc); err != nil {
		return nil, err
	}
	if err := frame.validateScope(frame.lifetime.facts.parent.Scope, tc); err != nil {
		return nil, err
	}
	child := freshProgramOwnerContext(
		"update_progress",
		definition.Contract,
		update.ExecutionID,
		frame.lifetime.facts.parent.Scope.ProjectID,
		frame.lifetime.facts.parent.SessionID,
		tc,
	)
	schema := e.Metadata.argsSchemaFor(ctx, child.Identity.SessionID, "update_progress")
	if schema == nil {
		return nil, fmt.Errorf("progress owner schema unavailable")
	}
	args, err := prepareNativeArguments(ctx, admission, update.Arguments)
	if err != nil {
		return nil, err
	}
	if reject := tools.ValidateCallArguments("update_progress", args.values, schema, child); reject != nil {
		args.close()
		return nil, reject
	}
	mutation, err := progress.PrepareMutation(args.values)
	if err != nil {
		args.close()
		return nil, progressOwnerError(err, "")
	}
	if store != nil && facts != nil {
		state, stateErr := facts.ProgramProgressState(ctx, frame.lifetime.facts.parent.SessionID)
		if stateErr != nil {
			args.close()
			return nil, stateErr
		}
		if state.SessionID != frame.lifetime.facts.parent.SessionID || state.RootSessionID == "" {
			args.close()
			return nil, fmt.Errorf("progress owner root identity unavailable")
		}
		snap, snapErr := store.Snapshot(ctx, state.RootSessionID)
		if snapErr != nil {
			args.close()
			return nil, snapErr
		}
		prepared.basis = progress.CaptureBasis(mutation, snap)
	}
	prepared.update = args
	prepared.admission = admission
	prepared.contractDigest = definition.Contract.Digest()
	prepared.identity = toolcontract.ProgramProgressIdentity{
		ProgramID:   frame.lifetime.facts.parent.Identity.ProgramID,
		ExecutionID: update.ExecutionID,
		ActorID:     frame.lifetime.facts.parent.ActorID,
		SessionID:   frame.lifetime.facts.parent.SessionID,
		Scope:       frame.lifetime.facts.parent.Scope,
	}
	held = true
	return prepared, nil
}

// Close releases review snapshots after application or abandonment.
func (p *PreparedProgramEnvelope) Close() {
	if p == nil || p.closed {
		return
	}
	p.closed = true
	if p.update != nil {
		p.update.close()
	}
	p.program = nil
	p.admission = nil
	p.basis = progress.PreparedProgressBasis{}
	p.sourceDigest = ""
	p.catalogDigest = ""
	p.contractDigest = ""
	p.identity = toolcontract.ProgramProgressIdentity{}
	if p.releaseFrame != nil {
		p.releaseFrame()
		p.releaseFrame = nil
	}
}

func (p *PreparedProgramEnvelope) validate(ctx context.Context) error {
	if p == nil || p.closed || p.program == nil {
		return fmt.Errorf("program envelope is closed")
	}
	if err := p.frame.alive(); err != nil {
		return err
	}
	source, catalog, err := p.program.ValidatedProgramIdentity(ctx)
	if err != nil {
		return err
	}
	if source != p.sourceDigest || catalog != p.catalogDigest {
		return fmt.Errorf("validated program identity changed")
	}
	return nil
}

func progressOwnerError(err error, root string) error {
	var rejected *progress.MutationRejection
	if errors.As(err, &rejected) {
		if root != "" && rejected.Data != nil {
			if snap, ok := rejected.Data["snapshot"].(progress.Snapshot); ok {
				rejected.Data["snapshot"] = progress.ForRequest(root, snap)
			}
		}
		return &toolrejection.ToolReject{Code: rejected.Code, OwnerRef: "progress", Data: rejected.Data}
	}
	return err
}
