package detectionpack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGeneratedPacksPresent(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	for id, sourceID := range map[string]string{
		"gcp-structured-actions":   "gcloud-cli",
		"azure-structured-actions": "azure-cli",
	} {
		if !config.Has(config.DetectionPacksDir.Join(id)) {
			t.Errorf("%s generated pack is missing", id)
			continue
		}
		pack, ok := cat.PackByID(id)
		source, sourceOK := cat.PackByID(sourceID)
		if !ok || !sourceOK || pack.Source != SourceGenerated || len(pack.Rules) != len(source.Rules) {
			t.Errorf("%s=%+v source=%+v", id, pack, source)
		}
	}
}

func TestAWSStructuredActionsUseReviewedRules(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	m := NewMatcher(cat)
	structuredEvent := func(action string) Event {
		return NewEvent(ActionObservation{
			Tool:        "mcp_aws_call",
			ToolArgs:    []string{"action=" + action},
			APIActions:  APIActionsFromStructuredArgs(map[string]any{"action": action}),
			EffectReach: EffectReachRemote,
		})
	}
	hit, matched := m.Match(structuredEvent("ec2:GetPasswordData"))
	if !matched || hit.PackID != "aws-cli" || hit.RuleTitle != "Read stored credential material" {
		t.Fatalf("secret read hit=%+v matched=%v; want the reviewed aws-cli secret-read rule", hit, matched)
	}
	if hit, matched := m.Match(structuredEvent("s3:GetObject")); matched {
		t.Fatalf("coarse read must stay silent, got %+v", hit)
	}
}

func TestGeneratedPackIDProtectedFromDeviceRemoval(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"gcp-structured-actions", "azure-structured-actions"} {
		err := RemoveDevicePack(t.TempDir(), shippedPacks(t), id)
		if !errors.Is(err, ErrPackNotRemovable) {
			t.Errorf("%s err=%v want ErrPackNotRemovable", id, err)
		}
	}
}

func TestDeviceCannotClaimGenerated(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	dir := filepath.Join(DevicePacksDir(cfg), "fake-generated")
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	testutil.FailErr(t, "pack.yaml", os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(`
id: fake-generated
label: Fake
description: Claims generated
source: generated
`), 0o600))
	testutil.FailErr(t, "rule", os.WriteFile(filepath.Join(dir, "rules", "hit.yml"), []byte(`
title: Hit
id: 66666666-6666-4666-8666-666666666666
description: d
logsource: {product: lycaon, service: tool_exec}
level: high
detection:
  sel: {Image: fakebin}
  condition: sel
`), 0o600))
	cat, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog", err)
	p, ok := cat.PackByID("fake-generated")
	if !ok {
		t.Fatal("missing pack")
	}
	if p.Source != SourceDevice {
		t.Fatalf("source=%s want device", p.Source)
	}
	found := false
	for _, w := range cat.Warnings {
		if strings.Contains(w, "claimed source generated") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected warning, got %v", cat.Warnings)
	}
}
