package projectsource

import "github.com/lycaon/lycaon/internal/toolcontract"

// Host lifecycle, backup and recovery operations own one bounded account. Command
// removals instead supply the existing program account at their journal entry.
func newSourceOperationMemory() (*toolcontract.ProgramMemory, error) {
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	if err != nil {
		return nil, err
	}
	return toolcontract.NewProgramMemory(limits)
}
