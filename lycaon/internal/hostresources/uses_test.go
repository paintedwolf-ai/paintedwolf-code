package hostresources

import (
	"reflect"
	"strings"
	"testing"
)

func catalogWithUses(uses string) []byte {
	return []byte(`version: 1
resources:
  - id: tool
    family: tools
    label: Tool
    category: Tools
    description: A tool.
    surfaces: [process_exec]
    realizations:
      - discover: {executable: {names: [tool]}}
        uses:
` + uses)
}

func TestCatalogRejectsMalformedUses(t *testing.T) {
	for name, uses := range map[string]string{
		"alone with another field":     "          - {id: start, label: start, requires: {capabilities: [host_execution, write_root], alone: true}}\n",
		"alone without host_execution": "          - {id: start, label: start, requires: {capabilities: [write_root], alone: true}}\n",
		"empty requirement":            "          - {id: start, label: start, requires: {ports: [80]}}\n",
		"unknown field":                "          - {id: start, label: start, requires: {capabilities: [root_shell]}}\n",
		"unknown role":                 "          - {id: start, label: start, roles: [admin], requires: {capabilities: [direct_ip]}}\n",
		"invalid id":                   "          - {id: Start, label: start, requires: {capabilities: [direct_ip]}}\n",
		"label with backtick":          "          - {id: start, label: \"`start`\", requires: {capabilities: [direct_ip]}}\n",
		"label too long":               "          - {id: start, label: " + strings.Repeat("x", useLabelBytes+1) + ", requires: {capabilities: [direct_ip]}}\n",
		"repeated id":                  "          - {id: start, label: a, requires: {capabilities: [direct_ip]}}\n          - {id: start, label: b, requires: {capabilities: [direct_ip]}}\n",
		"port out of range":            "          - {id: start, label: start, requires: {capabilities: [loopback_connect], ports: [70000]}}\n",
		"invalid alternative":          "          - {id: start, label: start, requires: {any_resource: [Docker]}}\n",
		"unknown key":                  "          - {id: start, label: start, note: x, requires: {capabilities: [direct_ip]}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCatalog(catalogWithUses(uses), "built-in"); err == nil {
				t.Fatal("catalog accepted a malformed use")
			}
		})
	}
	if _, err := ParseCatalog(catalogWithUses("          - {id: start, label: VM start, roles: [coordinator], requires: {capabilities: [host_execution], alone: true}}\n"), "built-in"); err != nil {
		t.Fatalf("valid use rejected: %v", err)
	}
}

func useState(id string, uses ...Use) State {
	return State{
		ID: id, Label: id, Category: "Tools", Status: StatusAvailable, HostSupport: HostSupported,
		Access: AccessAllow, Prompt: PromptOmit, Surfaces: []ExecutionSurface{SurfaceProcessExec}, Uses: uses,
	}
}

func TestMatchingUsesFollowTheSurfaceReach(t *testing.T) {
	vm := Use{ID: "vm", Label: "VM start", Roles: []AgentRole{RoleCoordinator}, Requires: UseRequirement{Capabilities: []string{"host_execution"}, Alone: true}}
	remote := Use{ID: "remote", Label: "remote TCP", Requires: UseRequirement{Capabilities: []string{"direct_ip"}}}
	stack := Use{ID: "stack", Label: "local stack", Requires: UseRequirement{AnyResource: []string{"docker", "podman"}}}
	tool := useState("tool", vm, remote, stack)
	docker := useState("docker")
	podman := useState("podman")
	podman.Access = AccessDeny
	snapshot := Snapshot{Resources: []State{tool, docker, podman}}

	labels := func(uses []AmbientUse) []string {
		var out []string
		for _, use := range uses {
			out = append(out, use.Label)
		}
		return out
	}
	coordinator := Reach{Role: RoleCoordinator, Offered: []string{"host_execution", "direct_ip"}}
	if got := labels(coordinator.MatchingUses(tool, snapshot)); !reflect.DeepEqual(got, []string{"VM start", "remote TCP", "local stack"}) {
		t.Fatalf("coordinator uses = %v", got)
	}
	worker := Reach{Role: RoleWorker, Offered: []string{"host_execution", "direct_ip"}}
	if got := labels(worker.MatchingUses(tool, snapshot)); !reflect.DeepEqual(got, []string{"remote TCP", "local stack"}) {
		t.Fatalf("worker uses = %v, want the coordinator-only use dropped", got)
	}
	narrow := Reach{Role: RoleCoordinator, Offered: []string{"direct_ip"}}
	if got := labels(narrow.MatchingUses(tool, snapshot)); !reflect.DeepEqual(got, []string{"remote TCP", "local stack"}) {
		t.Fatalf("uses without host_execution offered = %v", got)
	}
	stackUse := coordinator.MatchingUses(tool, snapshot)[2]
	if !reflect.DeepEqual(stackUse.Through, []string{"docker"}) {
		t.Fatalf("alternatives = %v, want only the reachable, allowed docker", stackUse.Through)
	}
	docker.Status = StatusUnavailable
	if got := labels(coordinator.MatchingUses(tool, Snapshot{Resources: []State{tool, docker, podman}})); !reflect.DeepEqual(got, []string{"VM start", "remote TCP"}) {
		t.Fatalf("uses with no reachable alternative = %v", got)
	}
}

func TestUsesAnnotateRowsWithoutDecidingThem(t *testing.T) {
	only := Use{ID: "vm", Label: "VM start", Roles: []AgentRole{RoleCoordinator}, Requires: UseRequirement{Capabilities: []string{"host_execution"}, Alone: true}}
	snapshot := Snapshot{Resources: []State{useState("colima", only)}}
	worker := ProjectAmbient(AmbientInput{
		Snapshot: snapshot, MutationCapable: true,
		Reach: Reach{Surfaces: []ExecutionSurface{SurfaceProcessExec}, Role: RoleWorker, Offered: []string{"loopback_connect"}},
	})
	if len(worker.Resources) != 1 || len(worker.Resources[0].Uses) != 0 {
		t.Fatalf("worker plan = %+v, want the row without the coordinator use", worker.Resources)
	}
	coordinator := ProjectAmbient(AmbientInput{
		Snapshot: snapshot, MutationCapable: true,
		Reach: Reach{Surfaces: []ExecutionSurface{SurfaceProcessExec}, Role: RoleCoordinator, Offered: []string{"host_execution"}},
	})
	if len(coordinator.Resources) != 1 || len(coordinator.Resources[0].Uses) != 1 {
		t.Fatalf("coordinator plan = %+v", coordinator.Resources)
	}
	if ambientLineCost(coordinator.Resources[0]) <= ambientLineCost(worker.Resources[0]) {
		t.Fatal("a rendered use costs nothing in the roster budget")
	}
}

func TestOfferedCapabilitiesFollowToolContracts(t *testing.T) {
	terminal := OfferedCapabilities([]string{"terminal_open"})
	for _, field := range []string{"host_execution", "direct_ip"} {
		for _, got := range terminal {
			if got == field {
				t.Fatalf("terminal_open offers %s outside its contract", field)
			}
		}
	}
	command := OfferedCapabilities([]string{"command", "terminal_open"})
	found := false
	for _, got := range command {
		found = found || got == "host_execution"
	}
	if !found {
		t.Fatalf("command offers %v without host_execution", command)
	}
}
