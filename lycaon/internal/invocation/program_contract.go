package invocation

import (
	"encoding/hex"
	"fmt"

	"github.com/lycaon/lycaon/internal/toolcontract"
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
	if p.LanguageVersion != toolcontract.LanguageVersion || p.CatalogDigest != toolcontract.LanguageCatalogDigest() {
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

// ChildStart freezes the definition and expanded-input reference before the
// existing per-operation admission boundary is reached. Each dynamic execution
// gets its own identity; a held resumption keeps the original one.
type ChildStart struct {
	ParentReceiptID    string
	Invocation         toolcontract.OperationInvocation
	Definition         toolcontract.OperationDefinition
	ArgumentsReference string
	ExpandedInputs     toolcontract.ReviewedOperationInputs
	Canonical          string
}

func (c ChildStart) Validate() error {
	if err := validateReviewedInputs(c.ExpandedInputs); err != nil {
		return err
	}
	if c.ParentReceiptID == "" || c.Invocation.Kind != toolcontract.ProgramChildInvocation {
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

// ChildSettlement retains the invoked domain's result without reclassifying
// effects from exit status, stdout, a checklist or a later aggregate stop.
type ChildSettlement struct {
	Result            toolcontract.ProducerResult
	HostStopCode      string
	OutputPublishable bool
	OwnerInvoked      bool
	DetailReference   string
	AcceptedRevision  string
	PublishedRevision string
}

func (c ChildSettlement) Validate() error {
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

// RetainedOutput identifies the actual byte-retention claim separately from
// operation provenance. A spill's initiating operation is not its claim marker.
type RetainedOutput struct {
	ExecutionID      string
	Stream           string
	Reference        string
	RetentionClaimID string
	Complete         bool
	Redacted         bool
	Mappings         []toolcontract.StreamSourceMapping
}
