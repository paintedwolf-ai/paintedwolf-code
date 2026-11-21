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
	}
}

func TestLoadDeclarationsMissingFile(t *testing.T) {
	t.Parallel()
	_, err := loadDeclarations(func(string) ([]byte, error) { return nil, errors.New("gone") })
	if err == nil || !strings.Contains(err.Error(), "read structure declarations") {
		t.Fatalf("err = %v", err)
	}
}

func TestSubsystemDeclarationRequiresDescription(t *testing.T) {
	t.Parallel()
	const entry = `
    entry_points:
        - github.com/lycaon/lycaon/internal/x
`
	described := "internal/x:\n    kind: subsystem\n    description: A concern." + entry
	if _, err := loadDeclarations(func(string) ([]byte, error) { return []byte(described), nil }); err != nil {
		t.Fatalf("subsystem with description: %v", err)
	}
	for name, description := range map[string]string{"absent": "", "blank": "\n    description: \"  \""} {
		raw := "internal/x:\n    kind: subsystem" + description + entry
		if _, err := loadDeclarations(func(string) ([]byte, error) { return []byte(raw), nil }); err == nil {
			t.Errorf("subsystem with %s description accepted", name)
		}
	}
}

func TestFamilyDeclarationAcceptsContractWithoutDescription(t *testing.T) {
	t.Parallel()
	raw := `internal/x:
    kind: family
    contract: github.com/lycaon/lycaon/internal/x/contract
`
	loaded, err := loadDeclarations(func(string) ([]byte, error) { return []byte(raw), nil })
	if err != nil {
		t.Fatalf("family with contract and no description: %v", err)
	}
	if got := loaded["internal/x"]; got.Kind != family || got.Contract != "github.com/lycaon/lycaon/internal/x/contract" {
		t.Fatalf("declaration = %#v", got)
	}
}
