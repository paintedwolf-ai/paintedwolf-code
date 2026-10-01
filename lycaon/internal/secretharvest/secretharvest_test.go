package secretharvest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fingerprinter(t *testing.T) *secretmatch.Fingerprinter {
	t.Helper()
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x2a}, 32))
	testutil.FailErr(t, "NewFingerprinter", err)
	return fp
}

func harvestFile(rt *secretharvest.Runtime, root, container, content string) []secretharvest.Value {
	return rt.Harvest(secretharvest.ContainerRead{RootSessionID: root, Container: container, Content: []byte(content)})
}

// Container parsing retains names and drops values below the match floor.
func TestHarvestParsesDotenvWithFloor(t *testing.T) {
	rt := secretharvest.NewRuntime(fingerprinter(t))
	values := harvestFile(rt, "sess", ".env",
		"# comment\nexport STRIPE_KEY=\"zebra-purple-42\"\nPORT=3000\nDEBUG=true\nDATABASE_URL=postgres://u:p@h/db\n")
	byName := map[string]secretharvest.Value{}
	for _, v := range values {
		byName[v.Name] = v
	}
	if _, ok := byName["STRIPE_KEY"]; !ok {
		t.Fatalf("unshapen value must harvest: %+v", values)
	}
	if _, ok := byName["DATABASE_URL"]; !ok {
		t.Fatalf("URL credential must harvest: %+v", values)
	}
	if _, ok := byName["PORT"]; ok {
		t.Fatal("a port is under the floor")
	}
	if _, ok := byName["DEBUG"]; ok {
		t.Fatal("a boolean is under the floor")
	}
	if byName["STRIPE_KEY"].Secret() != "zebra-purple-42" {
		t.Fatal("quotes must strip")
	}
	if byName["STRIPE_KEY"].Fingerprint == "" {
		t.Fatal("harvested values carry fingerprints")
	}
}

func TestHasMatchesHarvestedFingerprint(t *testing.T) {
	fp := fingerprinter(t)
	rt := secretharvest.NewRuntime(fp)
	values := harvestFile(rt, "sess", ".env", "SESSION_SECRET=zebra-purple-42\n")
	if len(values) != 1 {
		t.Fatalf("harvested %d values", len(values))
	}
	if !rt.Has("sess", values[0].Fingerprint) {
		t.Fatal("Has must see the harvested fingerprint")
	}
	if rt.Has("other", values[0].Fingerprint) {
		t.Fatal("Has must not leak across session trees")
	}
	if rt.Has("sess", fp.Fingerprint("not-harvested-value")) {
		t.Fatal("Has must miss an unknown fingerprint")
	}
}

// Declared values use the managed exact-match floor.
func TestRememberUsesLowerFloorForManagedValues(t *testing.T) {
	rt := secretharvest.NewRuntime(fingerprinter(t))
	const short = "Ky7-Lp"
	managed := rt.Remember("root-1", secretmatch.Remembered{
		Secret: short, Name: "deploy key", Origin: "managed secret",
		RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
		Source: secretmatch.SourceRememberedMatch, NonDisclosable: true,
	})
	if len(managed) != 1 || managed[0].Secret() != short {
		t.Fatalf("declared managed value was dropped below the harvested floor: %+v", managed)
	}
	guess := rt.Remember("root-1", secretmatch.Remembered{
		Secret: short + "!", Name: "MODE", Origin: ".env",
		Source: secretmatch.SourceRememberedMatch,
	})
	if len(guess) != 0 {
		t.Fatalf("undeclared value below the harvested floor was admitted: %+v", guess)
	}
}

// Bytes stronger evidence accounts for are not harvested as a new secret.
func TestHarvestSkipsHeldBytes(t *testing.T) {
	fp := fingerprinter(t)
	rt := secretharvest.NewRuntime(fp)
	authored := fp.Fingerprint("todo-web-client")
	values := rt.Harvest(secretharvest.ContainerRead{
		RootSessionID: "root", Container: ".env",
		Content: []byte("OIDC_CLIENT_ID=todo-web-client\nSESSION_SECRET=zebra-purple-42\n"),
		Held:    func(value secretmatch.SecretFingerprint) bool { return value == authored },
	})
	if len(values) != 1 || values[0].Name != "SESSION_SECRET" {
		t.Fatalf("harvested %+v, want only the unheld binding", values)
	}
	if rt.Has("root", authored) {
		t.Fatal("a held value entered the evidence base")
	}
}

// One byte string has one entry: a container read never replaces a managed
// value or drops its reference.
func TestHarvestKeepsStrongerEvidenceForTheSameBytes(t *testing.T) {
	rt := secretharvest.NewRuntime(fingerprinter(t))
	const secret = "Pw4-generated-value-9"
	const reference = "{{paintedwolf-secret:6f0c7f3e-0d59-4a55-9d64-1b2a3c4d5e6f}}"
	rt.Remember("root", secretmatch.Remembered{
		Secret: secret, Name: "db password", RuleID: secretmatch.ManagedRuleID,
		Title: secretmatch.ManagedRuleTitle, Reference: reference, NonDisclosable: true,
	})
	harvestFile(rt, "root", ".env", "POSTGRES_PASSWORD="+secret+"\n")
	values := rt.ValuesFor("root")
	if len(values) != 1 {
		t.Fatalf("values = %+v", values)
	}
	held := values[0]
	if held.RuleID != secretmatch.ManagedRuleID || !held.NonDisclosable || held.Reference != reference {
		t.Fatalf("container evidence replaced the managed entry: %+v", held)
	}
}

// A project-attributed screen reads only its own project's session trees.
func TestValuesForProjectScopesSessionTrees(t *testing.T) {
	rt := secretharvest.NewRuntime(fingerprinter(t))
	rt.SetProjectResolver(func(root string) string {
		if root == "remembered-root" {
			return "project-a"
		}
		return ""
	})
	rt.Harvest(secretharvest.ContainerRead{RootSessionID: "root-a", ProjectID: "project-a",
		Container: ".env", Content: []byte("A_SECRET=zebra-purple-42\n")})
	rt.Harvest(secretharvest.ContainerRead{RootSessionID: "root-b", ProjectID: "project-b",
		Container: ".env", Content: []byte("B_SECRET=orchard-violet-77\n")})
	rt.Remember("remembered-root", secretmatch.Remembered{Secret: "lantern-copper-51"})
	got := map[string]bool{}
	for _, v := range rt.ValuesForProject("project-a") {
		got[v.Secret()] = true
	}
	if !got["zebra-purple-42"] || !got["lantern-copper-51"] || got["orchard-violet-77"] || len(got) != 2 {
		t.Fatalf("project-a evidence = %v", got)
	}
}

func TestHarvestParsesJSONByKeyPath(t *testing.T) {
	rt := secretharvest.NewRuntime(fingerprinter(t))
	values := harvestFile(rt, "sess", "service-account.json", `{"auth":{"private_key_id":"abcdef1234567890"}}`)
	if len(values) != 1 || values[0].Name != "auth.private_key_id" {
		t.Fatalf("values = %+v", values)
	}
}

// Harvest evidence carries provenance and accepts fingerprint suppression.
func TestMatcherFusesHarvestEvidence(t *testing.T) {
	rt := secretharvest.NewRuntime(fingerprinter(t))
	harvestFile(rt, "root-1", ".env", "SESSION_SECRET=zebra-purple-42\n")

	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		out := []secretmatch.HarvestedValue{}
		for _, v := range rt.ValuesFor("root-1") {
			out = append(out, secretmatch.HarvestedValue{
				Name: v.Name, Container: v.Container, Secret: v.Secret(), Fingerprint: v.Fingerprint,
				RuleID: v.RuleID, Title: v.Title, Source: v.Source,
			})
		}
		return out
	})
	hits := m.Screen("payload with zebra-purple-42 inside")
	if len(hits) != 1 || hits[0].RuleID != secretmatch.HarvestRuleID {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].VarName != "SESSION_SECRET" || hits[0].Container != ".env" {
		t.Fatalf("provenance missing: %+v", hits[0])
	}
	if got := m.RedactString(context.Background(), "x zebra-purple-42 y"); got != "x [REDACTED] y" {
		t.Fatalf("redacted = %q", got)
	}

	suppressed := hits[0].Fingerprint
	m.SetIgnoredSource(func(context.Context) map[secretmatch.SecretFingerprint]bool {
		return map[secretmatch.SecretFingerprint]bool{suppressed: true}
	})
	if left := m.Screen("payload with zebra-purple-42 inside"); len(left) != 0 {
		t.Fatalf("an active public-value exception still matched: %+v", left)
	}
}

func TestExternalEvidenceSharesTheHarvestGenerationWithoutCopyingValues(t *testing.T) {
	runtime := secretharvest.NewRuntime(nil)
	var generations []uint64
	runtime.OnGrowth(func(root string, generation uint64) {
		if root != "root" {
			t.Errorf("unexpected root %q", root)
		}
		generations = append(generations, generation)
		// Observers run unlocked and may read the evidence.
		_ = runtime.ValuesFor(root)
	})
	runtime.Invalidate("root")
	runtime.Remember("root", secretmatch.Remembered{Secret: "orchard-harvest-value"})
	runtime.Invalidate("root")
	if len(generations) != 3 || generations[0] != 1 || generations[1] != 2 || generations[2] != 3 {
		t.Fatalf("external and harvested evidence clocks diverged: %v", generations)
	}
	if len(runtime.ValuesFor("root")) != 1 {
		t.Fatal("external invalidation duplicated protected values")
	}
}
