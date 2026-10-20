package toolcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// OperationFacts are compiled declarations. Runtime bindings and grants live
// outside this leaf. OAR uses Identity, not a fabricated model tool call.
type OperationFacts struct {
	Identity          string
	Version           int
	CatalogDigest     string
	Contract          Contract
	Arguments         json.RawMessage
	Commands          []CommandSpecification
	Streams           StreamContract
	Resources         []ResourceLimit
	ProgressGate      ProgressPolicy
	RejectionContract string
}

// ProgressPolicy selects the existing progress owner admission contract.
type ProgressPolicy string

// ProgressExistingAdmission preserves existing admission with separate attribution.
const (
	ProgressExistingAdmission ProgressPolicy = "existing_owner_admission"
	ProgressRequired          ProgressPolicy = "required"
	ProgressNotRequired       ProgressPolicy = "not_required"
)

func (p ProgressPolicy) Valid() bool {
	return p == ProgressExistingAdmission || p == ProgressRequired || p == ProgressNotRequired
}

// StreamContract distinguishes complete byte streams from display projections.
type StreamContract struct {
	Input    string
	Output   string
	Error    string
	Statuses map[int]string
}

// OperationDefinition is an immutable, digest-addressed compiled declaration.
// Facts returns a detached snapshot; neither registration nor lookup shares maps.
type OperationDefinition struct {
	data   string
	digest string
}

func NewOperationDefinition(facts OperationFacts) (OperationDefinition, error) {
	if facts.CatalogDigest == "" {
		facts.CatalogDigest = LanguageCatalogDigest()
	}
	if facts.CatalogDigest != LanguageCatalogDigest() {
		return OperationDefinition{}, fmt.Errorf("operation catalog digest mismatch")
	}
	if facts.Identity == "" || facts.Version < 1 || facts.Contract.Owner == "" || !facts.ProgressGate.Valid() || facts.RejectionContract == "" {
		return OperationDefinition{}, fmt.Errorf("operation identity, version, owner, progress and rejection contract required")
	}
	if strings.TrimSpace(facts.Identity) != facts.Identity || strings.TrimSpace(facts.Contract.Owner) != facts.Contract.Owner || facts.Contract.Lifecycle > LifecycleEphemeralControl {
		return OperationDefinition{}, fmt.Errorf("operation requires canonical identity, owner and valid lifecycle")
	}
	var schema map[string]json.RawMessage
	if err := DecodeLanguageJSON(facts.Arguments, &schema); err != nil || string(schema["type"]) != `"object"` {
		return OperationDefinition{}, fmt.Errorf("operation %s requires an object argument schema", facts.Identity)
	}
	if err := ValidateArgumentSchema(facts.Arguments); err != nil {
		return OperationDefinition{}, err
	}
	if facts.Streams.Output == "" || facts.Streams.Error == "" || len(facts.Streams.Statuses) == 0 {
		return OperationDefinition{}, fmt.Errorf("operation %s requires stream and status contracts", facts.Identity)
	}
	for _, command := range facts.Commands {
		if command.Owner != facts.Contract.Owner {
			return OperationDefinition{}, fmt.Errorf("operation %s command %s changes owner", facts.Identity, command.Name)
		}
	}
	for status := range facts.Streams.Statuses {
		if status < 0 || status > 255 {
			return OperationDefinition{}, fmt.Errorf("invalid operation stream status")
		}
	}
	for _, limit := range facts.Resources {
		if limit.ID == "" || limit.Maximum < 1 || limit.Unit == "" {
			return OperationDefinition{}, fmt.Errorf("invalid operation resource limit")
		}
	}
	data, err := json.Marshal(facts)
	if err != nil {
		return OperationDefinition{}, err
	}
	sum := sha256.Sum256(data)
	return OperationDefinition{data: string(data), digest: hex.EncodeToString(sum[:])}, nil
}

func (d OperationDefinition) Digest() string { return d.digest }

func (d OperationDefinition) Facts() OperationFacts {
	var facts OperationFacts
	// Construction validates the encoded declaration; the bytes cannot change.
	_ = json.Unmarshal([]byte(d.data), &facts)
	return facts
}

// Clone detaches mutable contract values at registry publication and lookup.
func (c Contract) Clone() Contract {
	c.SecretReferenceArgs = append([]string(nil), c.SecretReferenceArgs...)
	return c
}
