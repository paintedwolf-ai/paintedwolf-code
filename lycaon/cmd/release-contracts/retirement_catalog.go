package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
)

const retirementCatalogPath = "schemas/agent-public-retirements.json"

var vocabularyKinds = []string{"host_facts", "rejection_codes", "tools", "workflow_terms", "anchors", "themes"}

// The required dotted namespace keeps a bare tool name from declaring a contract.
var contentContractID = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

type vocabularyRetirements struct {
	FormatVersion    int                                        `json:"format_version"`
	ContentContracts map[string]contentContract                 `json:"content_contracts"`
	Vocabularies     map[string]map[string]retirementSuccessors `json:"vocabularies"`
}

type contentContract struct {
	Version int    `json:"version"`
	Summary string `json:"summary"`
}

type contentContractRef struct {
	ContentContract string `json:"content_contract"`
	Version         int    `json:"version"`
}

// retirementSuccessors holds exactly one form: same-vocabulary names or a declared content contract.
type retirementSuccessors struct {
	Names    []string
	Contract *contentContractRef
}

func (s *retirementSuccessors) UnmarshalJSON(raw []byte) error {
	switch trimmed := bytes.TrimSpace(raw); {
	case bytes.HasPrefix(trimmed, []byte("[")):
		return decodeStrict(trimmed, &s.Names)
	case bytes.HasPrefix(trimmed, []byte("{")):
		s.Contract = &contentContractRef{}
		return decodeStrict(trimmed, s.Contract)
	}
	return fmt.Errorf("retirement successor must be a name list or a content contract object")
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("retirement catalog must contain one JSON document")
	}
	return nil
}

func parseRetirements(raw []byte) (vocabularyRetirements, error) {
	var retired vocabularyRetirements
	if err := decodeStrict(raw, &retired); err != nil {
		return retired, err
	}
	if retired.FormatVersion != 1 || len(retired.Vocabularies) != len(vocabularyKinds) {
		return retired, fmt.Errorf("unsupported or incomplete agent-public retirement catalog")
	}
	for name, contract := range retired.ContentContracts {
		if !contentContractID.MatchString(name) {
			return retired, fmt.Errorf("content contract %q is not a namespaced identifier", name)
		}
		if contract.Version < 1 || strings.TrimSpace(contract.Summary) == "" {
			return retired, fmt.Errorf("content contract %s needs a positive version and a summary", name)
		}
	}
	for _, kind := range vocabularyKinds {
		if retired.Vocabularies[kind] == nil {
			return retired, fmt.Errorf("missing retirement vocabulary %s", kind)
		}
		for name, successors := range retired.Vocabularies[kind] {
			if err := resolveContract(kind, name, successors.Contract, retired.ContentContracts); err != nil {
				return retired, err
			}
		}
	}
	return retired, nil
}

func resolveContract(kind, name string, ref *contentContractRef, contracts map[string]contentContract) error {
	if ref == nil {
		return nil
	}
	declared, ok := contracts[ref.ContentContract]
	if !ok {
		return fmt.Errorf("retired %s %s names undeclared content contract %q", kind, name, ref.ContentContract)
	}
	if ref.Version != declared.Version {
		return fmt.Errorf("retired %s %s names content contract %s version %d, declared %d", kind, name, ref.ContentContract, ref.Version, declared.Version)
	}
	return nil
}

// checkReleasedRetirements keeps every record a release published.
func checkReleasedRetirements(previous, current vocabularyRetirements) error {
	for kind, names := range previous.Vocabularies {
		for name, successors := range names {
			if !reflect.DeepEqual(current.Vocabularies[kind][name], successors) {
				return fmt.Errorf("released retirement %s %s was removed", kind, name)
			}
		}
	}
	for name, prior := range previous.ContentContracts {
		contract, ok := current.ContentContracts[name]
		if !ok {
			return fmt.Errorf("released content contract %s was removed", name)
		}
		if contract.Version < prior.Version {
			return fmt.Errorf("released content contract %s regressed to version %d", name, contract.Version)
		}
	}
	return nil
}

// checkContractIdentities refuses a contract named after a tool, which would make it look callable.
func checkContractIdentities(contracts map[string]contentContract, before, after map[string]string, retired map[string]retirementSuccessors) error {
	for name := range contracts {
		_, released := before[name]
		_, active := after[name]
		_, retiring := retired[name]
		if released || active || retiring {
			return fmt.Errorf("content contract %s reuses a tool name", name)
		}
	}
	return nil
}
