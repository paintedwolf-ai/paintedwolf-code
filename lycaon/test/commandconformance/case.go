package commandconformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

// Case is one program, the workspace it starts from, and the userlands whose
// results it accepts.
type Case struct {
	ID          string
	Description string
	// Commands and Forms name the catalog commands and option spellings the case covers.
	Commands []string
	Forms    []string
	Program  string
	// Files are added to the shared seed workspace.
	Files map[string]referenceuserland.Entry
	// Tracked names the workspace paths whose final state is compared.
	Tracked []string
	// Git adds the recorded repository to the workspace.
	Git bool
	// CapabilityRequest is the JSON capability request sent with the program.
	CapabilityRequest string
	References        []referenceuserland.ID
}

var (
	allUserlands = []referenceuserland.ID{referenceuserland.GNU, referenceuserland.BSD, referenceuserland.BusyBox}
	// bashUserlands are the references for bash grammar, such as [[ ]], source,
	// PIPESTATUS, globstar, brace expansion and here-strings, and for tools busybox
	// lacks; busybox runs /bin/sh.
	bashUserlands = []referenceuserland.ID{referenceuserland.GNU, referenceuserland.BSD}
	// gnuAndBusyBox are the references for GNU spellings busybox shares.
	gnuAndBusyBox = []referenceuserland.ID{referenceuserland.GNU, referenceuserland.BusyBox}
	gnuUserland   = []referenceuserland.ID{referenceuserland.GNU}
	bsdUserland   = []referenceuserland.ID{referenceuserland.BSD}
)

// documentationWords ask a tool for its help, version or manual, which snapshots
// never record.
var documentationWords = []string{"--help", "--version", "info", "man"}

// Cases returns the corpus in a fixed order.
func Cases() []Case {
	return slices.Concat(languageCases, naturalCases, utilityCases, gitCases, liveRunCases)
}

// Validate reports a corpus that cannot be compared: duplicate identifiers,
// cases without references, or workspace paths outside the workspace.
func Validate(cases []Case) error {
	seen := map[string]bool{}
	for _, c := range cases {
		if c.ID == "" || seen[c.ID] {
			return fmt.Errorf("case %q: identifiers are required and unique", c.ID)
		}
		seen[c.ID] = true
		if len(c.References) == 0 {
			return fmt.Errorf("case %s compares with no userland", c.ID)
		}
		for _, id := range c.References {
			if !slices.Contains(referenceuserland.IDs, id) {
				return fmt.Errorf("case %s: unknown userland %q", c.ID, id)
			}
		}
		if c.CapabilityRequest != "" && !json.Valid([]byte(c.CapabilityRequest)) {
			return fmt.Errorf("case %s: capability request is not JSON", c.ID)
		}
		for _, word := range strings.Fields(c.Program) {
			if slices.Contains(documentationWords, strings.Trim(word, `'";|&()`)) {
				return fmt.Errorf("case %s asks for documentation (%s); snapshots record behaviour only", c.ID, word)
			}
		}
	}
	return nil
}

// Prelude enables the shell options the command language always has.
func Prelude(id referenceuserland.ID) string {
	if id == referenceuserland.BusyBox {
		return "set -o pipefail\n"
	}
	return "set -o pipefail\nshopt -s globstar\n"
}

// Workspace is the complete seeded workspace of the case.
func (c Case) Workspace(seed GitSeed) map[string]referenceuserland.Entry {
	entries := maps.Clone(seedWorkspace)
	maps.Copy(entries, c.Files)
	if c.Git {
		maps.Copy(entries, seed)
	}
	return entries
}

// Reference is the case as the runner runs it in userland id.
func (c Case) Reference(id referenceuserland.ID, seed GitSeed) referenceuserland.Case {
	return referenceuserland.Case{Program: Prelude(id) + c.Program, Workspace: c.Workspace(seed), Tracked: c.Tracked}
}

// Input identifies everything the reference run of the case depends on apart from the image.
func (c Case) Input(id referenceuserland.ID, seed GitSeed) string {
	data, err := json.Marshal(c.Reference(id, seed))
	if err != nil {
		panic(err) // A reference case is plain data.
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
