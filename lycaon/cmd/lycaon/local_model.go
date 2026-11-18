package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/clisocket"
	"github.com/lycaon/lycaon/internal/modelstore"
)

func runLocalModel(ctx context.Context, args []string, out io.Writer, send func(context.Context, clisocket.Request) (clisocket.Response, error)) error {
	var req clisocket.Request
	switch {
	case len(args) == 2 && args[0] == "import":
		path, err := filepath.Abs(args[1])
		if err != nil {
			return err
		}
		slot, err := modelstore.BundleSlot(ctx, path)
		if err != nil {
			return err
		}
		req = clisocket.Request{Op: clisocket.OpImportModel, Target: path, Model: slot.Model}
	case len(args) == 1 && args[0] == "opt-out":
		req.Op = clisocket.OpOptOutModel
	default:
		return errors.New("usage: pw local-model import <path> | pw local-model opt-out")
	}
	reply, err := send(ctx, req)
	if err != nil {
		return err
	}
	if reply.Status != clisocket.StatusOK {
		return fmt.Errorf("local model: %s", reply.Message)
	}
	_, err = fmt.Fprintln(out, "Request accepted. See Settings → AI providers for local model status.")
	return err
}
