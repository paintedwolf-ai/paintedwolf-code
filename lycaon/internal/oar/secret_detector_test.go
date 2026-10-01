package oar_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSecretDetectorFindings(t *testing.T) {
	m, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "BuildMatcher", err)
	d := oar.SecretMatchDetector{Matcher: m}
	gc := oar.NewGuardContext()
	gc.Content = "雪 leak AKIAQYJK5TXV4NZR7SGB in text"
	findings, err := d.Inspect(gc)
	testutil.FailErr(t, "Inspect", err)
	if len(findings) != 1 || findings[0].Fact != "secret_matches" {
		t.Fatalf("got %#v", findings)
	}
	list, ok := findings[0].Value.([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("want non-empty list, got %#v", findings[0].Value)
	}
	row, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("row type %T", list[0])
	}
	if row["rule_id"] != "gitleaks:aws-access-token" {
		t.Fatalf("rule_id=%v", row["rule_id"])
	}
	shape, _ := row["shape"].(string)
	if shape == "" || strings.Contains(shape, "AKIA") || strings.Contains(shape, "AKIAQYJK5TXV4NZR7SGB") {
		t.Fatalf("shape must be synthetic, got %q", shape)
	}
	start, okStart := row["start"].(int)
	end, okEnd := row["end"].(int)
	if !okStart || !okEnd || string([]rune(gc.Content)[start:end]) != "AKIAQYJK5TXV4NZR7SGB" {
		t.Fatalf("[OAR-OPS-14] invalid Unicode span: %#v", row)
	}
}

func TestOARLoadWithSecretDetector(t *testing.T) {
	root := filepath.Join("..", "..")
	profile := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host", "anchors", "oar-profile.yaml")
	raw, err := os.ReadFile(profile)
	testutil.FailErr(t, "read profile", err)
	if !strings.Contains(string(raw), "detector://secretmatch") {
		t.Fatal("oar-profile must list detector://secretmatch")
	}

	schemaDir := filepath.Join(root, "schemas")
	if _, err := os.Stat(schemaDir); err != nil {
		schemaDir = filepath.Join(root, "..", "schemas")
	}
	p, err := oar.LoadCapabilityDocument(profile)
	testutil.FailErr(t, "LoadCapabilityDocument", err)
	prev := oar.InstalledCapabilityDocument()
	oar.InstallCapabilityDocument(p)
	t.Cleanup(func() { oar.InstallCapabilityDocument(prev) })

	reg := oar.NewDetectorRegistry()
	reg.Register(oar.SecretMatchDetector{})
	l, err := oar.NewLoader(schemaDir)
	testutil.FailErr(t, "NewLoader", err)
	l.SetDetectors(reg)
	_, err = l.LoadEffectivePolicy()
	testutil.FailErr(t, "LoadStock", err)
}
