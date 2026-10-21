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

// ValidatedProgramIdentity is supplied by the actual sealed parsed program.
// Its retained-state lease must remain held through admission and application.
// Inspection-only plans, closed plans and unvalidated source cannot satisfy it.
type ValidatedProgramIdentity interface {
	ValidatedProgramIdentity(context.Context) (sourceDigest, catalogDigest string, err error)
}

// ProgramProgressUpdate is a distinct host invocation of the existing progress
// owner, attributed separately from program operations and the real model call.
type ProgramProgressUpdate struct {
	ExecutionID string
	Arguments   map[string]any
}

// PreparedProgramEnvelope owns only admitted immutable envelope facts. It never
// executes a program; the introducing command entry retains the parsed plan.
type PreparedProgramEnvelope struct {
	program        ValidatedProgramIdentity
	sourceDigest   string
	catalogDigest  string
	contractDigest string
	frame          ProgramFrame
	update         *nativeArguments
	admission      *toolcontract.ProgramMemory
	identity       toolcontract.ProgramProgressIdentity
	closed         bool
	attempted      bool
	releaseFrame   func()
}

// ProgramProgressResult records progress independently of later program success.
// A committed update cannot justify replaying an already reached program child.
type ProgramProgressResult struct {
	Identity     toolcontract.ProgramProgressIdentity
	OwnerInvoked bool
	Outcome      progress.AuthorUpdateResult
}

// PrepareProgramEnvelope validates both complete syntax and progress before
// either owner may apply an effect. A missing update preserves existing progress.
func (e *Executor) PrepareProgramEnvelope(ctx context.Context, frame ProgramFrame, program ValidatedProgramIdentity, update *ProgramProgressUpdate, admission *toolcontract.ProgramMemory, tc tools.ToolContext) (*PreparedProgramEnvelope, error) {
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
	prepared := &PreparedProgramEnvelope{program: program, sourceDigest: source, catalogDigest: catalog, frame: frame, releaseFrame: end}
	if update == nil {
		held = true
		return prepared, nil
	}
	definition, ok := e.definitionFor("update_progress")
	if !ok || definition.Contract.Owner == "" {
		return nil, fmt.Errorf("progress owner unavailable")
	}
	if strings.TrimSpace(update.ExecutionID) == "" || strings.TrimSpace(update.ExecutionID) != update.ExecutionID || strings.ContainsAny(update.ExecutionID, "\x00\x01") || update.ExecutionID == frame.lifetime.facts.parent.Identity.ExecutionID {
		return nil, fmt.Errorf("separate host progress execution identity required")
	}
	if err := frame.validateHostContext(ctx, tc); err != nil {
		return nil, err
	}
	if err := frame.validateScope(frame.lifetime.facts.parent.Scope, tc); err != nil {
		return nil, err
	}
	child := freshProgramOwnerContext("update_progress", definition.Contract, update.ExecutionID, frame.lifetime.facts.parent.Scope.ProjectID, frame.lifetime.facts.parent.SessionID, tc)
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
	content, _ := args.values["content"].(string)
	_, err = progress.PrepareAuthorUpdate(content)
	if err != nil {
		args.close()
		return nil, progressOwnerError(err)
	}
	prepared.update = args
	prepared.admission = admission
	prepared.contractDigest = definition.Contract.Digest()
	prepared.identity = toolcontract.ProgramProgressIdentity{ProgramID: frame.lifetime.facts.parent.Identity.ProgramID, ExecutionID: update.ExecutionID, ActorID: frame.lifetime.facts.parent.ActorID, SessionID: frame.lifetime.facts.parent.SessionID, Scope: frame.lifetime.facts.parent.Scope}
	held = true
	return prepared, nil
}

// Close releases review snapshots after application or abandonment. It does not
// close the caller's parsed plan, whose lifetime belongs to the command entry.
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

func progressOwnerError(err error) error {
	var rejected *progress.AuthorUpdateRejection
	if errors.As(err, &rejected) {
		return &toolrejection.ToolReject{Code: rejected.Code, OwnerRef: "progress", Data: rejected.Data}
	}
	return err
}
