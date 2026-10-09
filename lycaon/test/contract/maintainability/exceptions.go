package maintainability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

const exceptionDirectory = "lycaon/test/contract/maintainability-exceptions"

type artifactException struct {
	Category string `json:"category"`
	Artifact string `json:"artifact"`
	Cap      int    `json:"cap"`
	Reason   string `json:"reason"`
}

func loadExceptions(tree *workingTree, policy *sizebudget.Policy) error {
	if policy.Exceptions == nil {
		policy.Exceptions = map[string]map[string]sizebudget.Exception{}
	}
	for _, path := range tree.files() {
		if !strings.HasPrefix(path, exceptionDirectory+"/") {
			continue
		}
		if !tree.regular(path) || !strings.HasSuffix(path, ".json") {
			return fmt.Errorf("invalid exception file %s", path)
		}
		raw, err := tree.read(path)
		if err != nil {
			return fmt.Errorf("read exception: %w", err)
		}
		var record artifactException
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return fmt.Errorf("decode exception %s: %w", path, err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("exception %s has trailing content", path)
		}
		if record.Artifact == "" {
			return fmt.Errorf("empty artifact in %s", path)
		}
		if policy.Exceptions[record.Category] == nil {
			policy.Exceptions[record.Category] = map[string]sizebudget.Exception{}
		}
		if _, exists := policy.Exceptions[record.Category][record.Artifact]; exists {
			return fmt.Errorf("duplicate exception %s", record.Artifact)
		}
		policy.Exceptions[record.Category][record.Artifact] = sizebudget.Exception{Cap: record.Cap, Reason: record.Reason}
	}
	return policy.Validate(suite.CategoryNames())
}
