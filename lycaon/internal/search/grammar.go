package search

import (
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/config"
)

// Project scope resolver tokens for the optional project: filter.
const (
	ProjectScopeCurrent = "current"
)

var (
	dslFieldAllowlist, dslSingleValuedFields, dslFieldValues = mustLoadDSLGrammar()
)

// mustLoadDSLGrammar reads the vocabulary shared with Den's query linter.
func mustLoadDSLGrammar() ([]string, map[string]struct{}, map[string][]string) {
	raw, err := config.Read(config.SearchGrammar)
	if err != nil {
		panic(fmt.Sprintf("load search grammar: %v", err))
	}
	var catalog struct {
		Fields []struct {
			ID           string   `json:"id"`
			SingleValued bool     `json:"single_valued"`
			Values       []string `json:"values"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		panic(fmt.Sprintf("parse search grammar: %v", err))
	}
	seen := make(map[string]struct{}, len(catalog.Fields))
	fields := make([]string, 0, len(catalog.Fields))
	singleValued := make(map[string]struct{}, len(catalog.Fields))
	values := make(map[string][]string, 2)
	for _, row := range catalog.Fields {
		if row.ID == "" {
			panic("parse search grammar: empty field id")
		}
		if _, ok := seen[row.ID]; ok {
			panic(fmt.Sprintf("parse search grammar: duplicate field %q", row.ID))
		}
		seen[row.ID] = struct{}{}
		fields = append(fields, row.ID)
		if row.SingleValued {
			singleValued[row.ID] = struct{}{}
		}
		if len(row.Values) > 0 {
			values[row.ID] = row.Values
		}
	}
	if len(fields) == 0 {
		panic("parse search grammar: no fields")
	}
	return fields, singleValued, values
}

// ParseErrorKind is the typed parse-error taxonomy for the UI linter.
type ParseErrorKind string

const (
	ParseErrSyntax       ParseErrorKind = "syntax"
	ParseErrEmptyValue   ParseErrorKind = "empty_value"
	ParseErrInvalidValue ParseErrorKind = "invalid_value"
)

// ParseError contains query diagnostics without raw SQL.
type ParseError struct {
	Offset  int
	Message string
	Field   string
	Kind    ParseErrorKind
}

func (e *ParseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Field != "" {
		return fmt.Sprintf("%s at offset %d (field %s)", e.Message, e.Offset, e.Field)
	}
	return fmt.Sprintf("%s at offset %d", e.Message, e.Offset)
}

// DSLFieldAllowlist returns allowlisted DSL field names.
func DSLFieldAllowlist() []string {
	return append([]string(nil), dslFieldAllowlist...)
}
