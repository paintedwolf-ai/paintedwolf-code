package contract

import (
	"sort"
	"strings"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestBundledManifestPhasesReachable asserts every bundled workflow
// manifest is structurally sound:
//  1. Every non-terminal PhaseDef is reachable from firstPhase via Next
//     and/or transitions[].to — transition-only targets are not orphans.
//  2. Every Next / transitions[].to pointer targets a declared phase id.
//  3. At least one terminal phase exists.
//
// Linear Phases may stay next-only; off-spine choice targets live in PhaseDefs.
func TestBundledManifestPhasesReachable(t *testing.T) {
	t.Parallel()

	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	manifests := reg.All()
	if len(manifests) == 0 {
		t.Fatal("no bundled manifests found")
	}

	for key, m := range manifests {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			assertManifestReachability(t, m)
		})
	}
}

func assertManifestReachability(t *testing.T, m workflowdef.Manifest) {
	t.Helper()
	if m.Attach.Policy == workflowdef.AttachPolicySessionCreate {
		assertAmbientManifestGraph(t, m)
		return
	}
	defs := m.PhaseDefs
	if len(defs) == 0 {
		t.Fatal("manifest has no phase defs")
	}

	// Index by ID.
	byID := make(map[string]workflowdef.PhaseDef, len(defs))
	for _, d := range defs {
		if d.ID == "" {
			t.Fatal("phase with empty ID")
		}
		if _, dup := byID[d.ID]; dup {
			t.Fatalf("duplicate phase id %q", d.ID)
		}
		byID[d.ID] = d
	}

	// 1. Every Next / transitions[].to must target a declared phase id.
	for _, d := range defs {
		if d.Next != "" {
			if _, ok := byID[d.Next]; !ok {
				t.Fatalf("phase %q.Next = %q points at undeclared phase", d.ID, d.Next)
			}
		}
		for _, edge := range d.Transitions {
			to := strings.TrimSpace(edge.To)
			if to == "" {
				continue
			}
			if _, ok := byID[to]; !ok {
				t.Fatalf("phase %q transition %q.to = %q undeclared", d.ID, edge.ID, to)
			}
		}
	}

	// 2. BFS from firstPhase via next + transitions[].to (same keep-set as FinalizeManifest).
	start := firstPhaseID(m)
	if _, ok := byID[start]; !ok {
		t.Fatalf("firstPhase = %q is not a declared phase", start)
	}
	reachable := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if reachable[cur] {
			continue
		}
		def, ok := byID[cur]
		if !ok {
			continue
		}
		reachable[cur] = true
		if next := def.Next; next != "" && next != cur {
			if !reachable[next] {
				queue = append(queue, next)
			}
		}
		for _, edge := range def.Transitions {
			to := strings.TrimSpace(edge.To)
			if to == "" || reachable[to] {
				continue
			}
			queue = append(queue, to)
		}
	}

	// 3. Non-terminal declared phases must be reachable; terminal phases may be
	// host-closed (orchestration_complete) without a next-path (extends implement).
	var orphans []string
	for id, def := range byID {
		if reachable[id] || def.Terminal {
			continue
		}
		orphans = append(orphans, id)
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Fatalf("phases declared but never reachable from %q: %v", start, orphans)
	}

	// 4. At least one terminal phase (empty Next or Terminal flag).
	hasTerminal := false
	for _, d := range defs {
		if d.Next == "" || d.Terminal {
			hasTerminal = true
			break
		}
	}
	if !hasTerminal {
		t.Fatal("manifest has no terminal phase (no PhaseDef with empty Next or terminal: true)")
	}
}

func assertAmbientManifestGraph(t *testing.T, m workflowdef.Manifest) {
	t.Helper()
	work, ok := m.PhaseByID("work")
	if !ok || work.Next != "work" {
		t.Fatalf("ambient manifest work.next = %q want work loop", work.Next)
	}
	done, ok := m.PhaseByID("done")
	if !ok || !done.Terminal {
		t.Fatalf("ambient manifest done phase = %+v", done)
	}
	for _, id := range []string{"boot", "work", "done"} {
		if _, ok := m.PhaseByID(id); !ok {
			t.Fatalf("ambient manifest missing phase %q", id)
		}
	}
}

// firstPhaseID returns the first phase id for a manifest using the same
// rule as workflow.Manifest.firstPhase (unexported there). Falls back to
// the first PhaseDef when Phases is empty.
func firstPhaseID(m workflowdef.Manifest) string {
	if len(m.Phases) > 0 {
		return m.Phases[0]
	}
	if len(m.PhaseDefs) > 0 {
		return m.PhaseDefs[0].ID
	}
	return ""
}
