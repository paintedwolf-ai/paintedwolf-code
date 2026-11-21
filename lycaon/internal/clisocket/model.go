package clisocket

import (
	"context"
	"path/filepath"
)

// LocalModels accepts operations for the host's local model controller.
type LocalModels interface {
	ImportLocalModel(ctx context.Context, model, path string) error
	OptOutLocalModel(ctx context.Context) error
}

func (s *Server) handleModel(ctx context.Context, req Request) Response {
	if s.models == nil {
		return Response{Status: StatusError, Message: "Local model management is unavailable."}
	}
	var err error
	switch req.Op {
	case OpImportModel:
		if req.Model == "" || !filepath.IsAbs(req.Target) {
			return Response{Status: StatusError, Message: "Model import requires a model and an absolute bundle path."}
		}
		err = s.models.ImportLocalModel(ctx, req.Model, req.Target)
	case OpOptOutModel:
		if req.Target != "" || req.Model != "" {
			return Response{Status: StatusError, Message: "Opt-out takes no arguments."}
		}
		err = s.models.OptOutLocalModel(ctx)
	}
	if err != nil {
		return Response{Status: StatusError, Message: err.Error()}
	}
	return Response{Status: StatusOK}
}
