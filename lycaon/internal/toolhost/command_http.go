package toolhost

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/httpaction"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/native/curl"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// RegisterHTTP binds structured requests and command requests to the same owners.
func (r *Runtime) RegisterHTTP(deps httpaction.Deps) error {
	if r == nil {
		return fmt.Errorf("HTTP registration requires a host")
	}
	if err := httpaction.Register(r.Registry, deps); err != nil {
		return err
	}
	if r.Commands != nil {
		r.Commands.owners.http = deps
	}
	return nil
}

func (t programOwnerTools) httpOperation(servicesHTTP httpaction.Deps, mutations *mutation.Owners, barrier *native.PublicationBarrier) (command.NativeOperation, error) {
	httpDeps := t.http
	if httpDeps.Boundary == nil {
		httpDeps = servicesHTTP
	}
	if httpDeps.Boundary == nil {
		httpDeps.Boundary = t.boundary
	}
	if httpDeps.Boundary == nil {
		return command.NativeOperation{}, nil
	}
	owner, err := curl.NewOwners(httpDeps, mutations)
	if err != nil {
		return command.NativeOperation{}, err
	}
	return command.NativeOperation{Tool: owner.Operation(), Prepare: func(_ context.Context, frame *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
		prepared, err := owner.Prepare(mutation.Program{Paths: frame, Barrier: barrier}, request)
		if err != nil {
			return command.NativePrepared{}, err
		}
		return command.NativePrepared{Mode: prepared.Mode, Review: prepared.Review, Invoke: prepared.InvokeOwner}, nil
	}}, nil
}
