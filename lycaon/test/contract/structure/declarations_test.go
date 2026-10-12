package structure

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadDeclarations(t *testing.T) {
	t.Parallel()
	raw := `internal/session:
    kind: container
    description: Session orchestration hub.
internal/db:
    kind: subsystem
    description: SQL adapter.
    entry_points:
        - github.com/lycaon/lycaon/internal/db
`
	loaded, err := loadDeclarations(func(string) ([]byte, error) { return []byte(raw), nil })
	if err != nil {
		t.Fatalf("load declarations: %v", err)
	}
	if loaded["internal/session"].Kind != container || loaded["internal/db"].Kind != subsystem {
		t.Fatalf("kinds = %#v", loaded)
	}
}

func TestLoadDeclarationsRejectsInvalidShapes(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"unknown kind": `internal/x:
    kind: blob
`,
		"subsystem without entry points": `internal/x:
    kind: subsystem
    description: A concern.
`,
		"subsystem with foreign entry point": `internal/x:
    kind: subsystem
    description: A concern.
    entry_points:
        - github.com/someone/else/pkg
`,
		"container with entry points": `internal/x:
    kind: container
    entry_points:
        - github.com/lycaon/lycaon/internal/x
`,
		"family without contract": `internal/x:
    kind: family
`,
		"escaping path": `../outside:
    kind: container
`,
		"unknown field": `internal/x:
    kind: container
    reason: why not
`,
	}
	for name, raw := range cases {
		_, err := loadDeclarations(func(string) ([]byte, error) { return []byte(raw), nil })
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if !strings.Contains(err.Error(), name) && name == "unknown field" {
			t.Errorf("%s: wrong error %v", name, err)
		}
	}
}

func TestLoadDeclarationsMissingFile(t *testing.T) {
	t.Parallel()
	_, err := loadDeclarations(func(string) ([]byte, error) { return nil, errors.New("gone") })
	if err == nil || !strings.Contains(err.Error(), "read structure declarations") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeclarationDescriptionRequiredForSubsystemAndFamilyWithContract(t *testing.T) {
	t.Parallel()
	raw := `internal/x:
    kind: family
    contract: github.com/lycaon/lycaon/internal/x/contract
`
	loaded, err := loadDeclarations(func(string) ([]byte, error) { return []byte(raw), nil })
	if err != nil {
		t.Fatalf("family with contract: %v", err)
	}
	if loaded["internal/x"].Kind != family {
		t.Fatalf("kind = %q", loaded["internal/x"].Kind)
	}
}