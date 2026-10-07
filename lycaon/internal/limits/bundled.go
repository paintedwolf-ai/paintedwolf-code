package limits

import (
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/config"
	"gopkg.in/yaml.v3"
)

// Base cap used before the model window is known.
const baseWorkerSummaryMaxChars = 16384

// Shared bundled compaction limits.
var (
	DefaultWorkerSummaryMaxChars    int
	DefaultCitationGroundingRetries int
	DefaultWorkerGroundingRetries   int
	DefaultGroundingRejectsPerTurn  int
	DefaultGroundingRejectsPerCycle int
	DefaultCloseoutStallToolTurns   int
	DefaultReportDocumentRetries    int
	bundledCompactionLimitsOnce     sync.Once
	bundledCompactionLimitsErr      error
)

type bundledCompactionLimitsYAML struct {
	MaxWorkerSummaryChars           int `yaml:"max_worker_summary_chars"`
	MaxCitationGroundingRetries     int `yaml:"max_citation_grounding_retries"`
	MaxWorkerGroundingRetries       int `yaml:"max_worker_grounding_retries"`
	MaxGroundingRejectsPerTurn      int `yaml:"max_grounding_rejects_per_turn"`
	MaxGroundingRejectsPerCycle     int `yaml:"max_grounding_rejects_per_cycle"`
	MaxToolTurnsAfterCitationReject int `yaml:"max_tool_turns_after_citation_reject"`
	MaxReportDocumentRetries        int `yaml:"max_report_document_retries"`
}

func ensureBundledCompactionLimits() {
	bundledCompactionLimitsOnce.Do(func() {
		data, err := config.Read(config.Compaction)
		if err != nil {
			bundledCompactionLimitsErr = fmt.Errorf("read bundled compaction limits: %w", err)
			return
		}
		var doc bundledCompactionLimitsYAML
		// The llm package validates the complete compaction schema.
		if err := yaml.Unmarshal(data, &doc); err != nil {
			bundledCompactionLimitsErr = fmt.Errorf("parse bundled compaction limits: %w", err)
			return
		}
		if doc.MaxCitationGroundingRetries <= 0 ||
			doc.MaxWorkerGroundingRetries <= 0 ||
			doc.MaxGroundingRejectsPerTurn <= 0 ||
			doc.MaxGroundingRejectsPerCycle <= 0 ||
			doc.MaxToolTurnsAfterCitationReject <= 0 ||
			doc.MaxReportDocumentRetries <= 0 {
			bundledCompactionLimitsErr = fmt.Errorf("bundled compaction.yaml missing required limit fields")
			return
		}
		if doc.MaxWorkerSummaryChars > 0 {
			DefaultWorkerSummaryMaxChars = doc.MaxWorkerSummaryChars
		} else {
			DefaultWorkerSummaryMaxChars = baseWorkerSummaryMaxChars
		}
		DefaultCitationGroundingRetries = doc.MaxCitationGroundingRetries
		DefaultWorkerGroundingRetries = doc.MaxWorkerGroundingRetries
		DefaultGroundingRejectsPerTurn = doc.MaxGroundingRejectsPerTurn
		DefaultGroundingRejectsPerCycle = doc.MaxGroundingRejectsPerCycle
		DefaultCloseoutStallToolTurns = doc.MaxToolTurnsAfterCitationReject
		DefaultReportDocumentRetries = doc.MaxReportDocumentRetries
	})
	if bundledCompactionLimitsErr != nil {
		panic(bundledCompactionLimitsErr)
	}
}

func init() {
	ensureBundledCompactionLimits()
}
