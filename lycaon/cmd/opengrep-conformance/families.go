package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
)

type familyCounts struct {
	TruePositive  int `json:"true_positive"`
	FalsePositive int `json:"false_positive"`
	FalseNegative int `json:"false_negative"`
	Unresolved    int `json:"unresolved"`
}

type familySummary struct {
	Rule      string `json:"rule"`
	Language  string `json:"language"`
	Framework string `json:"framework"`
	evaluationSummary
}

func writeFamilySummaries(encoder *json.Encoder, measurements []projectMeasurement, admit opengrep.Mode, policy *admissionPolicy, partition string) error {
	type key struct {
		Mode                      opengrep.Mode
		Language, Framework, Rule string
	}
	groups := make(map[key][]projectMeasurement)
	for _, measurement := range measurements {
		for rule, counts := range measurement.Families {
			identity := key{measurement.Mode, measurement.Language, measurement.Framework, rule}
			copy := measurement
			copy.TruePositive, copy.FalsePositive, copy.FalseNegative = counts.TruePositive, counts.FalsePositive, counts.FalseNegative
			copy.Unresolved = counts.Unresolved
			groups[identity] = append(groups[identity], copy)
		}
	}
	keys := make([]key, 0, len(groups))
	for identity := range groups {
		keys = append(keys, identity)
	}
	slices.SortFunc(keys, func(a, b key) int {
		return strings.Compare(strings.Join([]string{string(a.Mode), a.Language, a.Framework, a.Rule}, "\x00"), strings.Join([]string{string(b.Mode), b.Language, b.Framework, b.Rule}, "\x00"))
	})
	var failures []error
	for _, identity := range keys {
		summary := summarizeProjects(identity.Mode, groups[identity])
		summary.Type = "family_summary"
		if identity.Mode == admit && policy != nil {
			summary.Rejection = policy.reject(summary)
			if len(summary.Rejection) > 0 {
				failures = append(failures, fmt.Errorf("%s/%s: %s", identity.Framework, identity.Rule, strings.Join(summary.Rejection, "; ")))
			}
		}
		if partition != "held_out" {
			summary.PrecisionLowerBound = nil
		}
		row := familySummary{Rule: identity.Rule, Language: identity.Language, Framework: identity.Framework, evaluationSummary: summary}
		if err := encoder.Encode(row); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}
