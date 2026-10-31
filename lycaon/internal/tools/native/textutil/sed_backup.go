package textutil

import (
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// sedBackup keeps the original staged backup and optional replay spool through
// publication. Seekable captures replay their original verified descriptor.
type sedBackup struct {
	stage   commandprogram.StagedOutput
	replay  io.ReadCloser
	spool   *scratch.ByteSpool
	working func()
	storage func()
}

func (b *sedBackup) Close() error {
	var err error
	if b.spool != nil {
		err = b.spool.Close()
		b.spool = nil
	}
	if b.storage != nil {
		b.storage()
		b.storage = nil
	}
	if b.working != nil {
		b.working()
		b.working = nil
	}
	b.replay = nil
	return err
}

func (r *sedRun) prepareBackup(operand commandprogram.Field, original io.ReadCloser, suffix string) (*sedBackup, error) {
	inv := r.inv
	release, err := inv.ports.Memory.Reserve(inv.ctx, (64<<10)+256)
	if err != nil {
		return nil, err
	}
	backup := &sedBackup{working: release, replay: original}
	backup.stage, err = inv.openDestination(commandprogram.Field{Value: backupName(operand.Value, suffix), Span: operand.Span})
	if err != nil {
		return backup, err
	}
	out := io.Writer(nativeutility.NewOutput(inv.ctx, backup.stage, "stream_bytes", inv.limit("stream_bytes")))
	seek, seekable := original.(io.Seeker)
	if !seekable {
		if inv.ports.Admission == nil {
			return backup, errors.New("sed backup replay requires program spill admission")
		}
		backup.storage, err = inv.ports.Admission.ReserveSpill(inv.ctx, inv.limit("spill_bytes"))
		if err != nil {
			return backup, err
		}
		backup.spool, err = scratch.NewByteSpool()
		if err != nil {
			return backup, err
		}
		out = io.MultiWriter(out, nativeutility.NewOutput(inv.ctx, backup.spool, "spill_bytes", inv.limit("spill_bytes")))
	}
	chunk := make([]byte, 64<<10)
	if _, err := io.CopyBuffer(out, original, chunk); err != nil {
		return backup, err
	}
	if seekable {
		_, err = seek.Seek(0, io.SeekStart)
		return backup, err
	}
	reader, err := backup.spool.Reader()
	if err != nil {
		return backup, err
	}
	backup.replay = io.NopCloser(reader)
	return backup, nil
}
