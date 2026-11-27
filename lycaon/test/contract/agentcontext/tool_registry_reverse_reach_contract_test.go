package contract

import (
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// recipientReach is one frozen request authority from the production sweep.
type recipientReach struct {
	label string
	auth  hoststate.TurnAuthority
}

func sweptRecipients(t *testing.T) []recipientReach {
	t.Helper()
	var out []recipientReach
	coord, err := hoststate.CoordinatorStates(t.Context())
	contractcheck.FailErr(t, "CoordinatorStates", err)
	for _, st := range coord {
		if st.Excluded {
			continue
		}
		auth, err := hoststate.FreezeTurnAuthority(t.Context(), st)
		contractcheck.FailErr(t, "FreezeTurnAuthority "+st.ID, err)
		out = append(out, recipientReach{label: "coordinator " + st.ID, auth: auth})
	}
	workers, err := hoststate.WorkerStates(t.Context())
	contractcheck.FailErr(t, "WorkerStates", err)
	for _, ws := range workers {
		if ws.Excluded {
			continue
		}
		auth, err := hoststate.FreezeWorkerAuthority(t.Context(), ws)
		contractcheck.FailErr(t, "FreezeWorkerAuthority "+ws.ID, err)
		out = append(out, recipientReach{label: "worker " + ws.ID, auth: auth})
	}
	return out
}

// commandForm is one catalog command that binds a command-only operation,
// with its declared effect modes.
type commandForm struct {
	command string
	modes   []string
}

func operationForms(t *testing.T) map[string][]commandForm {
	t.Helper()
	catalog, err := toolcontract.LanguageV1()
	contractcheck.FailErr(t, "LanguageV1", err)
	out := map[string][]commandForm{}
	for _, spec := range catalog.Commands {
		identities, err := toolcontract.CommandPolicyIdentities(spec.Name)
		contractcheck.FailErr(t, "CommandPolicyIdentities "+spec.Name, err)
		form := commandForm{command: spec.Name}
		for _, effect := range spec.Effects {
			form.modes = append(form.modes, effect.Mode)
		}
		for _, identity := range identities {
			if toolcontract.IsCommandOperation(identity) {
				out[identity] = append(out[identity], form)
			}
		}
	}
	return out
}

// Every registered callable definition reaches some swept recipient as an
// offered or loadable tool, and every command-only operation reaches one
// through command program reach in each declared effect mode, never as a tool
// offer of its own. A definition no recipient can reach is dead: retire it.
func TestRegisteredCapabilitiesJoinRecipientReach(t *testing.T) {
	reg := toolfixture.ContractServeBootRegistry(t)
	registerContractWorkerSurfaceTools(t, reg)
	recipients := sweptRecipients(t)
	if len(recipients) == 0 {
		t.Fatal("production sweep produced no recipients")
	}

	operations, err := toolcontract.CommandOperations()
	contractcheck.FailErr(t, "CommandOperations", err)
	forms := operationForms(t)

	var violations []string
	for _, meta := range reg.List() {
		name := meta.Name
		if toolcontract.IsCommandOperation(name) {
			violations = append(violations, fmt.Sprintf("command-only operation %s is registered as a callable definition", name))
			continue
		}
		reached := false
		for _, r := range recipients {
			if r.auth.Reach.Status(name) != toolreach.Absent {
				reached = true
				break
			}
		}
		if !reached {
			violations = append(violations, fmt.Sprintf("registered definition %s is never offered or loadable at a swept recipient", name))
		}
	}

	for _, op := range operations {
		if _, ok := reg.Operation(op); !ok {
			violations = append(violations, fmt.Sprintf("command-only operation %s has no registered operation metadata", op))
		}
		for _, r := range recipients {
			if slices.Contains(r.auth.Offered, op) || r.auth.PostLoadSchemas[op] != nil {
				violations = append(violations, fmt.Sprintf("command-only operation %s has a public tool offer at %s", op, r.label))
				break
			}
		}
		if len(forms[op]) == 0 {
			violations = append(violations, fmt.Sprintf("command-only operation %s binds no catalog command", op))
		}
		for _, form := range forms[op] {
			for _, mode := range form.modes {
				if !modeReached(recipients, form.command, mode) {
					violations = append(violations, fmt.Sprintf("command-only operation %s: command %s mode %s is admitted at no swept recipient", op, form.command, mode))
				}
			}
		}
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "registered capabilities without recipient reach", violations)
}

func modeReached(recipients []recipientReach, command, mode string) bool {
	for _, r := range recipients {
		if r.auth.Reach.Status(toolreach.ProgramTool) != toolreach.Absent && r.auth.ProgramTable.Mode(command, mode) {
			return true
		}
	}
	return false
}
