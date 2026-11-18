package catalogload

import (
	"sort"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/test/contract/laws"
)

var phaseGraph = []laws.Rule{{
	// Every bundled manifest's phase graph is sound: pointers name declared
	// phases, every phase is reachable from the first, and one is terminal.
	Name:    "bundled-manifest-phases-reachable",
	Anchors: []laws.Anchor{laws.Func("internal/workflow/definition", "RegistryFromDirs")},
	Check: func(r *laws.Run) {
		reg, err := workflowdef.RegistryFromDirs("")
		r.Must(err)
		all := reg.All()
		r.Expect(len(all), "bundled manifests")
		for key, m := range all {
			assertManifestReachability(func(format string, args ...any) { r.Report(key+": "+format, args...) }, m)
		}
	},
}}

func assertManifestReachability(fail func(string, ...any), m workflowdef.Manifest) {
	if m.Attach.Policy == workflowdef.AttachPolicySessionCreate {
		assertAmbientManifestGraph(fail, m)
		return
	}
	defs := m.PhaseDefs
	if len(defs) == 0 {
		fail("manifest has no phase defs")
		return
	}

	// Index by ID.
	byID := make(map[string]workflowdef.PhaseDef, len(defs))
	for _, d := range defs {
		if d.ID == "" {
			fail("phase with empty ID")
			return
		}
		if _, dup := byID[d.ID]; dup {
			fail("duplicate phase id %q", d.ID)
			return
		}
		byID[d.ID] = d
	}

	// 1. Every Next / transitions[].to must target a declared phase id.
	for _, d := range defs {
		if d.Next != "" {
			if _, ok := byID[d.Next]; !ok {
				fail("phase %q.Next = %q points at undeclared phase", d.ID, d.Next)
				return
			}
		}
		for _, edge := range d.Transitions {
			to := strings.TrimSpace(edge.To)
			if to == "" {
				continue
			}
			if _, ok := byID[to]; !ok {
				fail("phase %q transition %q.to = %q undeclared", d.ID, edge.ID, to)
				return
			}
		}
	}

	// 2. BFS from firstPhase via next + transitions[].to (same keep-set as FinalizeManifest).
	start := firstPhaseID(m)
	if _, ok := byID[start]; !ok {
		fail("firstPhase = %q is not a declared phase", start)
		return
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
		fail("phases declared but never reachable from %q: %v", start, orphans)
		return
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
		fail("manifest has no terminal phase (no PhaseDef with empty Next or terminal: true)")
		return
	}
}

func assertAmbientManifestGraph(fail func(string, ...any), m workflowdef.Manifest) {
	work, ok := m.PhaseByID("work")
	if !ok || work.Next != "work" {
		fail("ambient manifest work.next = %q want work loop", work.Next)
		return
	}
	done, ok := m.PhaseByID("done")
	if !ok || !done.Terminal {
		fail("ambient manifest done phase = %+v", done)
		return
	}
	for _, id := range []string{"boot", "work", "done"} {
		if _, ok := m.PhaseByID(id); !ok {
			fail("ambient manifest missing phase %q", id)
			return
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
