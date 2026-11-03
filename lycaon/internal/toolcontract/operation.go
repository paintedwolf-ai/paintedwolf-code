package toolcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

// Serialized returns the exact immutable definition bytes named by Digest.
// Receipt persistence must not decode and re-encode a different representation.
func (d OperationDefinition) Serialized() string { return d.data }

var operationFactsCache sync.Map

// ReadOnlyFacts returns the compiled operation declaration without copying
// its slices or maps. Callers must not mutate the returned value.
func (d OperationDefinition) ReadOnlyFacts() OperationFacts {
	if d.digest != "" {
		if cached, ok := operationFactsCache.Load(d.digest); ok {
			return cached.(OperationFacts)
		}
	}
	var facts OperationFacts
	// Construction validates the encoded declaration; the bytes cannot change.
	_ = json.Unmarshal([]byte(d.data), &facts)
	if d.digest != "" {
		operationFactsCache.Store(d.digest, facts)
	}
	return facts
}

func (d OperationDefinition) Facts() OperationFacts {
	facts := d.ReadOnlyFacts()
	if d.digest != "" {
		return cloneOperationFacts(facts)
	}
	return facts
}

func cloneOperationFacts(facts OperationFacts) OperationFacts {
	facts.Contract = facts.Contract.Clone()
	facts.Arguments = cloneRawBytes(facts.Arguments)
	if facts.Commands != nil {
		commands := make([]CommandSpecification, len(facts.Commands))
		for i, c := range facts.Commands {
			commands[i] = cloneCommandSpecification(c)
		}
		facts.Commands = commands
	}
	if facts.Streams.Statuses != nil {
		statuses := make(map[int]string, len(facts.Streams.Statuses))
		for k, v := range facts.Streams.Statuses {
			statuses[k] = v
		}
		facts.Streams.Statuses = statuses
	}
	facts.Resources = cloneResourceLimits(facts.Resources)
	return facts
}

// Clone detaches mutable contract values at registry publication and lookup.
func (c Contract) Clone() Contract {
	c.SecretReferenceArgs = cloneStringSlice(c.SecretReferenceArgs)
	return c
}
