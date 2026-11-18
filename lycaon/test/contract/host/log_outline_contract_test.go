package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// The log digest ships inside read tool-result JSON rather than a /v1/* response,
// so it has no HTTP route to conform-check; field parity with OpenAPI and Den is
// checked here instead.
func TestLogOutlineWireTypesRegistered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, spec := range []wirespec.DtoSyncSpec{
		{GoValue: api.LogDigest{}, OpenAPISchema: "LogDigest", TSInterface: "LogDigest"},
		{GoValue: api.ReadOutlineResponse{}, OpenAPISchema: "ReadOutlineResponse", TSInterface: "ReadOutlineResponse"},
	} {
		if err := wirespec.SyncDTOFields(root, spec); err != nil {
			t.Fatalf("%s: %v", spec.OpenAPISchema, err)
		}
	}
}

// Every LogFormat the outline declares has a parser and is a wire LogFormat
// value, and every wire value is declared.
func TestLogFormatInternalMatchesWire(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	enums, err := wirespec.DiscoverAPIStringEnums(root)
	contractcheck.FailErr(t, "discover API string enums", err)
	if len(enums["LogFormat"]) == 0 {
		t.Fatal("wire LogFormat enum not found")
	}
	corpus, err := contractcheck.LoadGoASTCorpusMode(filepath.Join(root, "lycaon", "internal", "logoutline"), parser.SkipObjectResolution)
	contractcheck.FailErr(t, "load logoutline corpus", err)
	var internal []string
	for _, file := range corpus.Production() {
		for _, decl := range file.AST.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				typ, ok := value.Type.(*ast.Ident)
				if !ok || typ.Name != "LogFormat" {
					continue
				}
				for _, v := range value.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if format, err := strconv.Unquote(lit.Value); err == nil && format != "" {
						internal = append(internal, format)
					}
				}
			}
		}
	}
	for _, format := range internal {
		if _, ok := logoutline.ParserFor(logoutline.LogFormat(format)); !ok {
			t.Errorf("declared log format %q has no parser", format)
		}
	}
	contractcheck.FailSetEqual(t, "logoutline formats vs wire LogFormat", internal, enums["LogFormat"])
}

func TestLogDigestInternalJSONMatchesWire(t *testing.T) {
	t.Parallel()
	assertSameJSONFields(t, "Digest", reflect.TypeOf(logoutline.Digest{}), reflect.TypeOf(api.LogDigest{}))
	assertSameJSONFields(t, "TimeSpan", reflect.TypeOf(logoutline.TimeSpan{}), reflect.TypeOf(api.LogDigestTimeSpan{}))
	assertSameJSONFields(t, "FieldStat", reflect.TypeOf(logoutline.FieldStat{}), reflect.TypeOf(api.LogDigestFieldStat{}))
	assertSameJSONFields(t, "Facet", reflect.TypeOf(logoutline.Facet{}), reflect.TypeOf(api.LogDigestFacet{}))
	assertSameJSONFields(t, "FacetValue", reflect.TypeOf(logoutline.FacetValue{}), reflect.TypeOf(api.LogDigestFacetValue{}))
	assertSameJSONFields(t, "Cluster", reflect.TypeOf(logoutline.Cluster{}), reflect.TypeOf(api.LogDigestCluster{}))
}

func assertSameJSONFields(t *testing.T, label string, internal, wire reflect.Type) {
	t.Helper()
	internalFields := wirespec.JsonFieldNames(internal)
	wireFields := wirespec.JsonFieldNames(wire)
	if !contractcheck.SortedSetEqual(internalFields, wireFields) {
		t.Fatalf("%s JSON fields drift:\ninternal: %v\nwire: %v", label, internalFields, wireFields)
	}
}
