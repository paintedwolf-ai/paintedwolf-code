package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	noteContracts = `"content_contracts":{"paintedwolf.markdown_note":{"version":1,"summary":"Published Markdown."}},`
	noteSuccessor = `{"content_contract":"paintedwolf.markdown_note","version":1}`
	noteToolPath  = packsRoot + "/painted-wolf/platform/tools/schemas/retired_note.yaml"
)

func retirementCatalog(contracts, toolEntries string) []byte {
	return []byte(`{"format_version":1,` + contracts + `"vocabularies":{"host_facts":{},"rejection_codes":{},"tools":{` + toolEntries + `},"workflow_terms":{},"anchors":{},"themes":{}}}`)
}

func contractRelease(catalog []byte, withNote bool) treeRelease {
	tree := publicVocabularyFixture()
	tree[capabilityPath] = []byte("host_facts: []\n")
	tree["schemas/guidance_registry.json"] = []byte(`{"hint_codes":{}}`)
	tree[retirementCatalogPath] = catalog
	if withNote {
		tree[noteToolPath] = []byte("schema: {}\n")
	}
	return tree
}

func TestRetiredToolResolvesToDeclaredContentContract(t *testing.T) {
	released := contractRelease(retirementCatalog("", ""), true)
	for _, tc := range []struct {
		name, contracts, entries, want string
		keepTool                       bool
		extraTool                      string
	}{
		{name: "AC-COMPAT-03 retired tool points to declared contract", contracts: noteContracts, entries: `"retired_note":` + noteSuccessor},
		{name: "AC-COMPAT-03 removal needs a successor", contracts: noteContracts, want: "without a named successor"},
		{name: "AC-COMPAT-03 unknown contract", contracts: noteContracts, entries: `"retired_note":{"content_contract":"paintedwolf.other_note","version":1}`, want: "undeclared content contract"},
		{name: "AC-COMPAT-03 wrong version", contracts: noteContracts, entries: `"retired_note":{"content_contract":"paintedwolf.markdown_note","version":2}`, want: "version 2, declared 1"},
		{name: "AC-COMPAT-03 missing version", contracts: noteContracts, entries: `"retired_note":{"content_contract":"paintedwolf.markdown_note"}`, want: "version 0, declared 1"},
		{name: "AC-COMPAT-03 empty object", contracts: noteContracts, entries: `"retired_note":{}`, want: "undeclared content contract"},
		{name: "AC-COMPAT-03 unknown field", contracts: noteContracts, entries: `"retired_note":{"content_contract":"paintedwolf.markdown_note","version":1,"alias":"x"}`, want: "unknown field"},
		{name: "AC-COMPAT-03 wait as undeclared contract", contracts: noteContracts, entries: `"retired_note":{"content_contract":"wait","version":1}`, want: "undeclared content contract"},
		{name: "AC-COMPAT-03 active tool as undeclared contract", contracts: noteContracts, entries: `"retired_note":{"content_contract":"read","version":1}`, want: "undeclared content contract"},
		{name: "AC-COMPAT-03 wait declared as contract", contracts: `"content_contracts":{"wait":{"version":1,"summary":"Not a contract."}},`, entries: `"retired_note":{"content_contract":"wait","version":1}`, want: "not a namespaced identifier"},
		{name: "AC-COMPAT-03 contract is not a string successor", contracts: noteContracts, entries: `"retired_note":["paintedwolf.markdown_note"]`, want: "unavailable successor"},
		{name: "AC-COMPAT-03 contract named after active tool", contracts: `"content_contracts":{"paintedwolf.tool":{"version":1,"summary":"Shadow."}},`, entries: `"retired_note":{"content_contract":"paintedwolf.tool","version":1}`, extraTool: "paintedwolf.tool", want: "reuses a tool name"},
		{name: "AC-COMPAT-03 contract named after released tool", contracts: `"content_contracts":{"retired.note":{"version":1,"summary":"Shadow."}},`, entries: `"retired_note":{"content_contract":"retired.note","version":1},"retired.note":["read"]`, want: "reuses a tool name"},
		{name: "AC-COMPAT-03 retired tool stays absent", contracts: noteContracts, entries: `"retired_note":` + noteSuccessor, keepTool: true, want: "was reused"},
		{name: "string successor in same vocabulary", entries: `"retired_note":["read"]`},
		{name: "string successor missing", entries: `"retired_note":["gone"]`, want: "unavailable successor"},
		{name: "empty string list", entries: `"retired_note":[]`, want: "no named successor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := contractRelease(retirementCatalog(tc.contracts, tc.entries), tc.keepTool)
			if tc.extraTool != "" {
				candidate[packsRoot+"/painted-wolf/platform/tools/schemas/"+tc.extraTool+".yaml"] = []byte("schema: {}\n")
			}
			err := checkAgentVocabulary(released, candidate)
			if tc.want == "" {
				testutil.FailErr(t, "check retirement", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("check=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestReleasedContentContractRetirementIsPermanent(t *testing.T) {
	released := contractRelease(retirementCatalog(noteContracts, `"retired_note":`+noteSuccessor), false)
	for _, tc := range []struct {
		name, candidate, want string
	}{
		{name: "AC-COMPAT-03 unchanged", candidate: string(retirementCatalog(noteContracts, `"retired_note":`+noteSuccessor))},
		{name: "AC-COMPAT-03 retirement removed", candidate: string(retirementCatalog(noteContracts, "")), want: "released retirement"},
		{name: "AC-COMPAT-03 successor rewritten as a tool", candidate: string(retirementCatalog(noteContracts, `"retired_note":["read"]`)), want: "released retirement"},
		{name: "AC-COMPAT-03 contract removed", candidate: string(retirementCatalog("", "")), want: "released retirement"},
		{name: "AC-COMPAT-03 contract version advanced under a pinned retirement", candidate: string(retirementCatalog(`"content_contracts":{"paintedwolf.markdown_note":{"version":2,"summary":"Published Markdown."}},`, `"retired_note":`+noteSuccessor)), want: "version 1, declared 2"},
		{name: "AC-COMPAT-03 summary is display copy", candidate: string(retirementCatalog(`"content_contracts":{"paintedwolf.markdown_note":{"version":1,"summary":"Reworded."}},`, `"retired_note":`+noteSuccessor))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkAgentVocabulary(released, contractRelease([]byte(tc.candidate), false))
			if tc.want == "" {
				testutil.FailErr(t, "check released retirement", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("check=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestReleasedContentContractDeclarationIsPermanent(t *testing.T) {
	released, err := parseRetirements(retirementCatalog(`"content_contracts":{"paintedwolf.markdown_note":{"version":2,"summary":"Published Markdown."}},`, ""))
	testutil.FailErr(t, "parse released catalog", err)
	for _, tc := range []struct{ name, contracts, want string }{
		{name: "advanced", contracts: `"content_contracts":{"paintedwolf.markdown_note":{"version":3,"summary":"Published Markdown."}},`},
		{name: "removed", want: "was removed"},
		{name: "regressed", contracts: `"content_contracts":{"paintedwolf.markdown_note":{"version":1,"summary":"Published Markdown."}},`, want: "regressed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, err := parseRetirements(retirementCatalog(tc.contracts, ""))
			testutil.FailErr(t, "parse candidate catalog", err)
			err = checkReleasedRetirements(released, current)
			if tc.want == "" {
				testutil.FailErr(t, "check released contract", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("check=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestRetirementCatalogRefusesMalformedContentContracts(t *testing.T) {
	for name, raw := range map[string][]byte{
		"declaration unknown field":    retirementCatalog(`"content_contracts":{"paintedwolf.note":{"version":1,"summary":"Note.","callable":false}},`, ""),
		"declaration without summary":  retirementCatalog(`"content_contracts":{"paintedwolf.note":{"version":1}},`, ""),
		"declaration blank summary":    retirementCatalog(`"content_contracts":{"paintedwolf.note":{"version":1,"summary":" "}},`, ""),
		"declaration zero version":     retirementCatalog(`"content_contracts":{"paintedwolf.note":{"version":0,"summary":"Note."}},`, ""),
		"declaration string version":   retirementCatalog(`"content_contracts":{"paintedwolf.note":{"version":"1","summary":"Note."}},`, ""),
		"tool shaped declaration":      retirementCatalog(`"content_contracts":{"surface_note":{"version":1,"summary":"Note."}},`, ""),
		"mcp shaped declaration":       retirementCatalog(`"content_contracts":{"mcp_*":{"version":1,"summary":"Note."}},`, ""),
		"unknown vocabulary":           []byte(`{"format_version":1,"vocabularies":{"host_facts":{},"rejection_codes":{},"tools":{},"workflow_terms":{},"anchors":{},"palettes":{}}}`),
		"null successor":               retirementCatalog(noteContracts, `"old":null`),
		"scalar successor":             retirementCatalog(noteContracts, `"old":"read"`),
		"mixed successor list":         retirementCatalog(noteContracts, `"old":["read",`+noteSuccessor+`]`),
		"object list":                  retirementCatalog(noteContracts, `"old":[`+noteSuccessor+`]`),
		"contract and names":           retirementCatalog(noteContracts, `"old":{"content_contract":"paintedwolf.markdown_note","version":1,"names":["read"]}`),
		"contract in other vocabulary": []byte(`{"format_version":1,"vocabularies":{"host_facts":{"old":{"content_contract":"paintedwolf.other","version":1}},"rejection_codes":{},"tools":{},"workflow_terms":{},"anchors":{},"themes":{}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRetirements(raw); err == nil {
				t.Fatalf("malformed catalog passed: %s", raw)
			}
		})
	}
	parsed, err := parseRetirements(retirementCatalog(noteContracts, `"old":["read"],"older":`+noteSuccessor))
	testutil.FailErr(t, "parse both successor forms", err)
	tools := parsed.Vocabularies["tools"]
	if len(tools["old"].Names) != 1 || tools["old"].Contract != nil || tools["older"].Names != nil || tools["older"].Contract == nil {
		t.Fatalf("successor forms mixed: %+v", tools)
	}
}

func TestShippedRetirementCatalogDeclaresMarkdownNote(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "..", filepath.FromSlash(retirementCatalogPath)))
	testutil.FailErr(t, "read shipped catalog", err)
	catalog, err := parseRetirements(raw)
	testutil.FailErr(t, "parse shipped catalog", err)
	if contract := catalog.ContentContracts["paintedwolf.markdown_note"]; contract.Version != 1 {
		t.Fatalf("markdown note contract=%+v", contract)
	}
	if successors := catalog.Vocabularies["host_facts"]["paintedwolf.unparsed_json"]; len(successors.Names) != 2 || successors.Contract != nil {
		t.Fatalf("string successors changed form: %+v", successors)
	}
}

// AC-COMPAT-03: a name list passes the checker whenever its names exist, so only
// the shipped entries can show these names retire to the content contract.
func TestShippedRetirementsOfNoteNameTheContentContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "..", filepath.FromSlash(retirementCatalogPath)))
	testutil.FailErr(t, "read shipped catalog", err)
	catalog, err := parseRetirements(raw)
	testutil.FailErr(t, "parse shipped catalog", err)
	for _, tc := range []struct{ kind, name string }{
		{"tools", "surface_note"},
		{"host_facts", "paintedwolf.surface_note_offered"},
		{"rejection_codes", "SURFACE_NOTE_WORKER_SESSION"},
	} {
		got, ok := catalog.Vocabularies[tc.kind][tc.name]
		if !ok || len(got.Names) != 0 || got.Contract == nil || got.Contract.ContentContract != "paintedwolf.markdown_note" {
			t.Errorf("%s %s = %+v, want the markdown note contract", tc.kind, tc.name, got)
		}
	}
	// The refusal a model sees reads the manifest; both must name the same tools.
	manifest, err := nativemanifest.Load()
	testutil.FailErr(t, "load native manifest", err)
	want := map[string]string{}
	for name, successors := range catalog.Vocabularies["tools"] {
		if successors.Contract != nil {
			want[name] = successors.Contract.ContentContract
		}
	}
	if !reflect.DeepEqual(manifest.RetiredToContentContract, want) {
		t.Fatalf("manifest %v, retirement catalog %v", manifest.RetiredToContentContract, want)
	}
}
