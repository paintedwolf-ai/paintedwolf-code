package search

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

const searchPinBatchSize = 1024

func (inv *invocation) recordPin(revision toolcontract.ExecutionRevision) error {
	if inv.owners.Pins == nil || inv.request.Invocation.Identity.ExecutionID == "" {
		return fmt.Errorf("search reads require a program execution and pin recorder")
	}
	if err := revision.Validate(); err != nil {
		return err
	}
	if memory := inv.owners.Revisions.Memory; memory != nil {
		size := int64(512 + 2*(len(revision.Role)+len(revision.Mode)+len(revision.Basis)+len(revision.RootID)+len(revision.Path)+len(revision.DocumentID)+len(revision.DiskSHA256)+len(revision.Representation)+len(revision.ContentSHA256)))
		release, err := memory.ReserveProgramState(inv.ctx, size)
		if err != nil {
			return err
		}
		inv.pinReleases = append(inv.pinReleases, release)
	}
	revision.Role = toolcontract.ExecutionRevisionRole(strings.Clone(string(revision.Role)))
	revision.Mode = toolcontract.ExecutionRevisionMode(strings.Clone(string(revision.Mode)))
	revision.Basis = toolcontract.ExecutionRevisionBasis(strings.Clone(string(revision.Basis)))
	revision.Representation = toolcontract.SourceRepresentation(strings.Clone(string(revision.Representation)))
	revision.RootID = strings.Clone(revision.RootID)
	revision.Path = strings.Clone(revision.Path)
	revision.DocumentID = strings.Clone(revision.DocumentID)
	revision.DiskSHA256 = strings.Clone(revision.DiskSHA256)
	revision.ContentSHA256 = strings.Clone(revision.ContentSHA256)
	inv.pins = append(inv.pins, revision)
	if len(inv.pins) >= searchPinBatchSize {
		return inv.flushPins()
	}
	return nil
}

// flushPins advances the durable ordinal and releases the completed page.
func (inv *invocation) flushPins() error {
	if len(inv.pins) == 0 {
		return nil
	}
	if inv.owners.Pins == nil || inv.request.Invocation.Identity.ExecutionID == "" {
		return fmt.Errorf("search reads require a program execution and pin recorder")
	}
	if err := inv.owners.Pins.RecordRevisionPins(inv.ctx, inv.request.Invocation.Identity.ExecutionID, inv.flushedPins, inv.pins); err != nil {
		return err
	}
	inv.flushedPins += len(inv.pins)
	inv.closePins()
	return nil
}
func (inv *invocation) closePins() {
	inv.pins = nil
	for _, release := range inv.pinReleases {
		release()
	}
	inv.pinReleases = nil
}
