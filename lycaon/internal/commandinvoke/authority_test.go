package commandinvoke

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func authorityFixtureFrame(t *testing.T) *contribframe.Frame {
	t.Helper()
	set, err := contribution.Compile(contribution.CompileInput{
		Units: []contribution.Input{
			{
				UnitID: "contributions/commands/go-home", Kind: contribution.KindCommand,
				ProviderPackID: "acme/reviewer",
				Body:           []byte("id: acme/reviewer:go-home\ntitle: Go home\naction: {kind: navigate, destination: home}\n"),
			},
			{
				UnitID: "contributions/commands/fix", Kind: contribution.KindCommand,
				ProviderPackID: "acme/reviewer",
				Body:           []byte("id: acme/reviewer:fix\ntitle: Fix\naction: {kind: editor_action, ref: acme/reviewer:fix-action}\n"),
			},
			{
				UnitID: "contributions/editor-actions/fix-action", Kind: contribution.KindEditorAction,
				ProviderPackID: "acme/reviewer",
				Body:           []byte("id: acme/reviewer:fix-action\ntitle: Fix finding\ntarget: {kind: finding, required: true}\nexecution: {preset: fix_finding, prompt_ref: guidance/fix}\n"),
			},
			{
				UnitID: "contributions/commands/inspect", Kind: contribution.KindCommand,
				ProviderPackID: "acme/reviewer",
				Body:           []byte("id: acme/reviewer:inspect\ntitle: Inspect\naction: {kind: editor_action, ref: acme/reviewer:inspect-action}\n"),
			},
			{
				UnitID: "contributions/editor-actions/inspect-action", Kind: contribution.KindEditorAction,
				ProviderPackID: "acme/reviewer",
				Body:           []byte("id: acme/reviewer:inspect-action\ntitle: Inspect file\ntarget: {kind: file, required: true}\nexecution: {preset: inspect_file, prompt_ref: guidance/inspect}\n"),
			},
		},
		UnitProvider: func(string) (string, bool) { return "acme/reviewer", true },
	})
	testutil.FailErr(t, "compile", err)
	frame, err := contribframe.Build(&catalogview.View{
		Catalog: &extpacks.EffectiveCatalog{
			Revision: "cat-1",
			Loaded:   map[string]extpacks.UnitEffective{},
			Packs: []extpacks.PackSummary{
				{ID: "acme/reviewer", Contributing: true},
			},
		},
		Contributions: set,
	}, &mcp.ResourceGeneration{Revision: "mcp-1"})
	testutil.FailErr(t, "build frame", err)
	return frame
}

func inputFor(t *testing.T, frame *contribframe.Frame, commandID string) Input {
	t.Helper()
	id, err := contribution.ParseID(commandID)
	testutil.FailErr(t, "parse", err)
	command, ok := frame.Command(id)
	if !ok {
		t.Fatalf("command %s not in fixture frame", commandID)
	}
	return Input{Frame: frame, CommandID: id, Command: command, ProjectID: "p1", SessionID: "s1"}
}

func TestAuthorityAdmitsTrustedPackKinds(t *testing.T) {
	frame := authorityFixtureFrame(t)
	if err := (PolicyAuthority{}).Authorize(context.Background(), inputFor(t, frame, "acme/reviewer:go-home")); err != nil {
		t.Fatalf("trusted pack navigate must authorize: %v", err)
	}
}

func TestAuthorityEditorPresetCeilings(t *testing.T) {
	frame := authorityFixtureFrame(t)
	base := inputFor(t, frame, "acme/reviewer:fix")

	missingTarget := base
	if err := (PolicyAuthority{}).Authorize(context.Background(), missingTarget); err == nil {
		t.Fatal("fix_finding without a structured target must be rejected")
	}

	missingFinding := base
	missingFinding.Context = api.CommandInvokeContext{Path: "main.go"}
	if err := (PolicyAuthority{}).Authorize(context.Background(), missingFinding); err == nil {
		t.Fatal("fix_finding without a finding id must be rejected")
	}

	unverified := base
	unverified.Context = api.CommandInvokeContext{Path: "main.go", FindingID: "f-1"}
	err := (PolicyAuthority{
		FindingNamesPath: func(_ context.Context, sessionID, path string) (bool, error) {
			return sessionID == "s1" && path == "other.go", nil
		},
	}).Authorize(context.Background(), unverified)
	if err == nil || !strings.Contains(err.Error(), "recorded finding") {
		t.Fatalf("a finding that does not name the path must reject, got %v", err)
	}

	verified := base
	verified.Context = api.CommandInvokeContext{Path: "main.go", FindingID: "f-1", DocumentRevision: 4}
	authority := PolicyAuthority{
		FindingNamesPath: func(_ context.Context, _, path string) (bool, error) { return path == "main.go", nil },
		DocumentRevision: func(_ context.Context, _, _, _ string) (int64, bool, error) { return 4, true, nil },
	}
	if err := authority.Authorize(context.Background(), verified); err != nil {
		t.Fatalf("verified finding + matching revision must authorize: %v", err)
	}

	stale := verified
	stale.Context.DocumentRevision = 3
	if err := authority.Authorize(context.Background(), stale); err == nil {
		t.Fatal("a stale document revision must be rejected")
	}

	omitted := verified
	omitted.Context.DocumentRevision = 0
	err = authority.Authorize(context.Background(), omitted)
	if err == nil || !strings.Contains(err.Error(), "tracked document revision") {
		t.Fatalf("a writing preset without a revision must reject, got %v", err)
	}

	noSource := verified
	err = (PolicyAuthority{
		FindingNamesPath: func(_ context.Context, _, path string) (bool, error) { return path == "main.go", nil },
	}).Authorize(context.Background(), noSource)
	if err == nil || !strings.Contains(err.Error(), "no document source") {
		t.Fatalf("an unconfigured document source must reject, got %v", err)
	}
}

func TestAuthorityReadOnlyPresetRevisionIsOptionalButVerified(t *testing.T) {
	frame := authorityFixtureFrame(t)
	base := inputFor(t, frame, "acme/reviewer:inspect")

	noRevision := base
	noRevision.Context = api.CommandInvokeContext{Path: "main.go"}
	if err := (PolicyAuthority{}).Authorize(context.Background(), noRevision); err != nil {
		t.Fatalf("inspect_file without a revision must authorize: %v", err)
	}

	stale := base
	stale.Context = api.CommandInvokeContext{Path: "main.go", DocumentRevision: 2}
	err := (PolicyAuthority{
		DocumentRevision: func(_ context.Context, _, _, _ string) (int64, bool, error) { return 7, true, nil },
	}).Authorize(context.Background(), stale)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a supplied stale revision must reject even read-only, got %v", err)
	}
}
