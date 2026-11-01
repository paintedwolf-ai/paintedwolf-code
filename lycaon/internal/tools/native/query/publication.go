package query

import (
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// queryWorking owns one operation's existing working-memory lease. Shrinking
// transfers a parser's admitted capacity to its surviving result without an
// unadmitted allocation or overlapping reservations for the same bytes.
type queryWorking struct {
	inv   *invocation
	bytes int64
}

func (w *queryWorking) resize(n int64) error {
	memory := w.inv.owners.Revisions.Memory
	if memory != nil {
		if n > w.bytes {
			if err := memory.ReserveBytes(w.inv.ctx, n-w.bytes); err != nil {
				return err
			}
		} else {
			memory.ReleaseBytes(w.bytes - n)
		}
	}
	w.bytes = n
	return w.inv.ctx.Err()
}
func (w *queryWorking) Close() {
	if memory := w.inv.owners.Revisions.Memory; memory != nil {
		memory.ReleaseBytes(w.bytes)
	}
	w.bytes = 0
}

func (inv *invocation) publishBytes(source *source, destination toolcontract.ExecutionRevision, data []byte) error {
	work := &queryWorking{inv: inv}
	defer work.Close()
	if err := work.resize(int64(len(data))); err != nil {
		return err
	}
	return inv.publishText(source, destination, string(data), work)
}

// publishText preserves an accepted document's authority or a disk file's
// captured encoding. Disk bytes use the reviewed, recoverable write door;
// the whole-file bound does not change editor mutation limits.
func (inv *invocation) publishText(source *source, destination toolcontract.ExecutionRevision, text string, work *queryWorking) error {
	maximum := inv.owners.limit("whole_file_bytes")
	if int64(len(text)) > maximum {
		return tools.CommandLimitExceeded("whole_file_bytes", maximum, int64(len(text)))
	}
	if err := work.resize(int64(len(text))); err != nil {
		return err
	}
	input := native.WriteInput{Path: source.abs, Content: text, Admission: native.WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.revision}, Memory: inv.owners.Revisions.Memory}
	sourceBytes, err := textfile.EncodedSize(source.text, source.encoding)
	if err != nil {
		return err
	}
	encodedBytes, err := textfile.EncodedSize(text, source.encoding)
	if err != nil {
		return err
	}
	preserveTextGates := source.revision.Mode == toolcontract.AcceptedDocumentRevision || (int64(len(text)) <= readcaps.MaxMutationBytes && sourceBytes <= readcaps.MaxMutationBytes && encodedBytes <= readcaps.MaxMutationBytes)
	if !preserveTextGates {
		encodedBound, err := textfile.EncodedSize(text, source.encoding)
		if err != nil {
			return err
		}
		if encodedBound > maximum {
			return tools.CommandLimitExceeded("whole_file_bytes", maximum, encodedBound)
		}
		release := func() {}
		if memory := inv.owners.Revisions.Memory; memory != nil {
			var err error
			release, err = memory.ReserveProgramState(inv.ctx, encodedBound)
			if err != nil {
				return err
			}
		}
		defer release()
		encoded, err := textfile.EncodeBounded(text, source.encoding, textfile.LimitsForRaw(maximum))
		if err != nil {
			return err
		}
		input.Exact, input.Bytes = true, encoded
	}
	result, err := inv.owners.Write.Apply(inv.ctx, input, inv.tc)
	inv.published = inv.published || result.Accepted || result.Saved
	if err != nil {
		return err
	}
	return nil
}
