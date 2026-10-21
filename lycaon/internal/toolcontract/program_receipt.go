package toolcontract

import (
	"encoding/hex"
	"fmt"
)

// ProgramVersion identifies one submitted program without retaining an
// interpreter continuation or manufacturing another model tool call.
type ProgramVersion struct {
	ProgramID       string
	ParentReceiptID string
	RootExecutionID string
	LanguageVersion int
	CatalogDigest   string
	SourceUnitID    string
	SourceDigest    string
	SourceReference string
}

func (p ProgramVersion) Validate() error {
	if p.ProgramID == "" || p.ParentReceiptID == "" || p.RootExecutionID == "" || p.SourceUnitID == "" || p.SourceReference == "" {
		return fmt.Errorf("program receipt requires parent and retained source identities")
	}
	if p.LanguageVersion != LanguageVersion || p.CatalogDigest != LanguageCatalogDigest() {
		return fmt.Errorf("program receipt does not match the frozen language catalog")
	}
	var digest [32]byte
	if len(p.SourceDigest) != 64 {
		return fmt.Errorf("program receipt requires an exact source digest")
	}
	if _, err := hex.Decode(digest[:], []byte(p.SourceDigest)); err != nil {
		return fmt.Errorf("program source digest: %w", err)
	}
	return nil
}

// ProgramSource records an exact capture. Recording input is not a delivered read.
type ProgramSource struct {
	UnitID          string
	Pin             SourcePin
	DetailReference string
}

// ProgramNode describes known syntax, not a speculative loop iteration.
type ProgramNode struct {
	ID           string
	SourceUnitID string
	Span         SourceSpan
}

// ProgramStart retains a validated plan and exact source before child admission.
// PlanReference and SourceReference name screened durable bodies, not inline text.
// The receipt owner validates it against its own trace ceilings.
type ProgramStart struct {
	Version         ProgramVersion
	PlanReference   string
	SourceExact     bool
	SyntaxValidated bool
	Sources         []ProgramSource
	Nodes           []ProgramNode
}

// ProgramChildStart freezes the definition and expanded-input reference before
// the existing per-operation admission boundary is reached. Each dynamic
// execution gets its own identity; a held resumption keeps the original one.
type ProgramChildStart struct {
	ParentReceiptID    string
	Invocation         OperationInvocation
	Definition         OperationDefinition
	ArgumentsReference string
	ExpandedInputs     ReviewedOperationInputs
	Canonical          string
}

// Validate checks structure; the receipt owner also bounds encoded input size.
func (c ProgramChildStart) Validate() error {
	if !c.ExpandedInputs.Valid() {
		return fmt.Errorf("reviewed inputs require admitted known lossless representation")
	}
	if c.ParentReceiptID == "" || c.Invocation.Kind != ProgramChildInvocation {
		return fmt.Errorf("child receipt requires an actual parent and host execution identity")
	}
	if err := c.Invocation.Validate(); err != nil {
		return err
	}
	facts := c.Definition.Facts()
	if c.Definition.Digest() == "" || facts.CatalogDigest != c.Invocation.CatalogDigest {
		return fmt.Errorf("child receipt requires its frozen offered definition")
	}
	if c.ArgumentsReference == "" {
		return fmt.Errorf("child receipt requires retained expanded-input identity")
	}
	return nil
}

// ProgramChildSettlement retains the invoked domain's result without
// reclassifying effects from exit status, stdout, a checklist or a later stop.
type ProgramChildSettlement struct {
	Result            ProducerResult
	HostStopCode      string
	OutputPublishable bool
	OwnerInvoked      bool
	DetailReference   string
	AcceptedRevision  string
	PublishedRevision string
}

func (c ProgramChildSettlement) Validate() error {
	if err := c.Result.Validate(); err != nil {
		return err
	}
	if c.OutputPublishable && (!c.Result.PublicationEligible || c.HostStopCode != "") {
		return fmt.Errorf("stopped or ineligible output is not publishable")
	}
	if c.DetailReference == "" {
		return fmt.Errorf("child settlement requires retained authoritative detail")
	}
	return nil
}

// ProgramSettlement is the process owner's aggregate statement. Child evidence
// remains independently queryable and is never rewritten by this transition.
type ProgramSettlement struct {
	State           string
	Status          *int
	StopCode        string
	DetailReference string
}

func (s ProgramSettlement) Validate() error {
	switch s.State {
	case "completed", "rejected", "failed", "interrupted":
	default:
		return fmt.Errorf("terminal program state required")
	}
	if s.Status != nil && (*s.Status < 0 || *s.Status > 255) {
		return fmt.Errorf("invalid aggregate utility status")
	}
	if s.DetailReference == "" {
		return fmt.Errorf("aggregate requires retained detail")
	}
	return nil
}
