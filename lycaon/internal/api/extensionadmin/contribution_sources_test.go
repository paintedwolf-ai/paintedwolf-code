package extensionadmin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDecodeProviderChoicesIsStrictAndBounded(t *testing.T) {
	choices, err := decodeProviderChoices(`{"choices":[{"id":"one","label":"One","description":"Plain <b>text</b>"}]}`)
	testutil.FailErr(t, "decode choices", err)
	if len(choices) != 1 || choices[0].ID != "one" {
		t.Fatalf("choices = %+v", choices)
	}
	for _, raw := range []string{
		`{"choices":[{"id":"one","label":"One","command":"run"}]}`,
		`{"choices":[{"id":"one","label":"One"},{"id":"one","label":"Again"}]}`,
		`{"choices":[],"unknown":true}`,
		`{"choices":[]} trailing`,
	} {
		if _, err := decodeProviderChoices(raw); err == nil {
			t.Errorf("invalid choice envelope accepted: %s", raw)
		}
	}
}

func TestDecodeProviderSearchResultsEnforcesProjectionAndActivationSchema(t *testing.T) {
	source := &contribution.SearchSource{
		Query:      contribution.SearchQuery{MaxResults: 2},
		Result:     contribution.SearchResultProjection{ID: "issue_id", Title: "title", Description: "repository", Arguments: "activation"},
		Activation: contribution.SearchActivation{Input: []contribution.OutputField{{ID: "issue-id", Type: contribution.PropertyString, Required: true}}},
	}
	results, err := decodeProviderSearchResults(`{"results":[{"issue_id":"I-1","title":"Ship","repository":"acme/app","activation":{"issue-id":"I-1"}}]}`, source)
	testutil.FailErr(t, "decode search results", err)
	if len(results) != 1 || results[0].Arguments["issue-id"] != "I-1" {
		t.Fatalf("results = %+v", results)
	}
	for _, raw := range []string{
		`{"results":[{"issue_id":"I-1","title":"Ship","repository":"a","activation":{"issue-id":"I-1"},"url":"https://evil.test"}]}`,
		`{"results":[{"issue_id":"I-1","title":"Ship","repository":"a","activation":{"issue-id":2}}]}`,
		`{"results":[{"issue_id":"I-1","title":"Ship","repository":"a","activation":{"issue-id":"I-1","extra":true}}]}`,
		`{"results":[{"issue_id":"I-1","title":"Ship","repository":"a","activation":"I-1"}]}`,
	} {
		if _, err := decodeProviderSearchResults(raw, source); err == nil {
			t.Errorf("invalid search envelope accepted: %s", raw)
		}
	}
}

func TestProviderEnvelopeCapsBytesRowsAndText(t *testing.T) {
	if _, err := decodeProviderChoices(strings.Repeat("x", contributionProviderEnvelopeBytes+1)); err == nil {
		t.Fatal("oversized provider response accepted")
	}
	rows := make([]string, contribution.MaxChoicesPerStep+1)
	for index := range rows {
		rows[index] = fmt.Sprintf(`{"id":"%d","label":"Choice"}`, index)
	}
	if _, err := decodeProviderChoices(`{"choices":[` + strings.Join(rows, ",") + `]}`); err == nil {
		t.Fatal("oversized choice collection accepted")
	}
	if _, err := decodeProviderChoices(`{"choices":[{"id":"one","label":"` + strings.Repeat("x", 257) + `"}]}`); err == nil {
		t.Fatal("oversized choice label accepted")
	}
}
