package main

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
)

func projectEvaluationModes(selected, admit opengrep.Mode) ([]opengrep.Mode, error) {
	if selected == "" {
		return []opengrep.Mode{opengrep.Intraprocedural, opengrep.Intrafile}, nil
	}
	if err := (opengrep.Analysis{Mode: selected}).Validate(); err != nil {
		return nil, err
	}
	if admit != "" && selected != admit {
		return nil, fmt.Errorf("admission mode %s is not selected for evaluation", admit)
	}
	return []opengrep.Mode{selected}, nil
}
