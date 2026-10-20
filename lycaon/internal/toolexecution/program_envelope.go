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
	admission      ProgramStateAdmission
	identity       toolcontract.ProgramProgressIdentity
	closed         bool
	attempted      bool
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
func (e *Executor) PrepareProgramEnvelope(ctx context.Context, frame ProgramFrame, program ValidatedProgramIdentity, update *ProgramProgressUpdate, admission ProgramStateAdmission, tc tools.ToolContext) (*PreparedProgramEnvelope, error) {
	if err := frame.alive(); err != nil {
		return nil, err
	}
	if e == nil || program == nil {
		return nil, fmt.Errorf("sealed program and executor required")
	}
	source, catalog, err := program.ValidatedProgramIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if len(source) != 64 || catalog != frame.parent.CatalogDigest {
		return nil, fmt.Errorf("validated program identity changed")
	}
	prepared := &PreparedProgramEnvelope{program: program, sourceDigest: source, catalogDigest: catalog, frame: frame}
	if update == nil {
		return prepared, nil
	}
	definition, ok := e.definitionFor("update_progress")
	if !ok || definition.Contract.Owner == "" {
		return nil, fmt.Errorf("progress owner unavailable")
	}
	if strings.TrimSpace(update.ExecutionID) == "" || strings.TrimSpace(update.ExecutionID) != update.ExecutionID || strings.ContainsAny(update.ExecutionID, "\x00\x01") || update.ExecutionID == frame.parent.Identity.ExecutionID {
		return nil, fmt.Errorf("separate host progress execution identity required")
	}
	if err := frame.validateHostContext(ctx, tc); err != nil {
		return nil, err
	}
	if err := frame.validateScope(frame.parent.Scope, tc); err != nil {
		return nil, err
	}
	child := freshProgramOwnerContext("update_progress", definition.Contract, update.ExecutionID, frame.parent.Scope.ProjectID, frame.parent.SessionID, tc)
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
	prepared.identity = toolcontract.ProgramProgressIdentity{ProgramID: frame.parent.Identity.ProgramID, ExecutionID: update.ExecutionID, ActorID: frame.parent.ActorID, SessionID: frame.parent.SessionID, Scope: frame.parent.Scope}
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
