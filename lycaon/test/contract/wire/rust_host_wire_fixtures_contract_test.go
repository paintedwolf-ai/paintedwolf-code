package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// rustHostWireFixturePath is read by lycaon-den/src-tauri/tests/host_wire_mirrors.rs.
const rustHostWireFixturePath = "lycaon-den/src-tauri/tests/host_wire_fixtures.json"

const rustHostWireRefresh = "UPDATE_RUST_HOST_WIRE_FIXTURES=1 ./task test:contract -- ./test/contract/wire/..."

type rustHostWireFixture struct {
	Source   string           `json:"source"`
	Examples []map[string]any `json:"examples"`
}

type rustHostWireFixtureFile struct {
	Generated    string                         `json:"generated"`
	Fixtures     map[string]rustHostWireFixture `json:"fixtures"`
	Vocabularies map[string][]string            `json:"vocabularies"`
}

// TestRustHostWireFixturesMatchGoTypes keeps the Tauri shell's host-wire
// fixtures generated from the Go types the host actually writes.
func TestRustHostWireFixturesMatchGoTypes(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	fixtures := map[string]rustHostWireFixture{
		"error_response":                 goTypeFixture(t, "pkg/api.ErrorResponse", wire.ErrorResponse{}),
		"attachment_upload_response":     goTypeFixture(t, "pkg/api.AttachmentUploadResponse", wire.AttachmentUploadResponse{}),
		"attachment_video_facts":         goTypeFixture(t, "pkg/api.AttachmentVideoFacts", wire.AttachmentVideoFacts{}),
		"presence_challenge":             goTypeFixture(t, "pkg/api.PresenceChallenge", wire.PresenceChallenge{}),
		"managed_secret_reveal_response": goTypeFixture(t, "pkg/api.ManagedSecretRevealResponse", wire.ManagedSecretRevealResponse{}),
		"daemon_manifest":                goTypeFixture(t, "internal/api.DaemonManifest", api.DaemonManifest{}),
		"startup_record":                 startupRecordFixture(t),
	}
	phases := make([]string, 0, len(startupprotocol.Phases()))
	for _, phase := range startupprotocol.Phases() {
		phases = append(phases, string(phase))
	}
	file := rustHostWireFixtureFile{
		Generated:    "Generated from Go host types by lycaon/test/contract/wire/rust_host_wire_fixtures_contract_test.go. Do not edit; refresh with " + rustHostWireRefresh,
		Fixtures:     fixtures,
		Vocabularies: map[string][]string{"startup_phase": phases},
	}
	want, err := json.MarshalIndent(file, "", "  ")
	contractcheck.FailErr(t, "encode Rust host-wire fixtures", err)
	want = append(want, '\n')

	path := filepath.Join(root, rustHostWireFixturePath)
	if os.Getenv("UPDATE_RUST_HOST_WIRE_FIXTURES") == "1" {
		contractcheck.FailErr(t, "write Rust host-wire fixtures", os.WriteFile(path, want, 0o644))
		return
	}
	got, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read "+rustHostWireFixturePath, err)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: the Tauri shell's host-wire fixtures must match the Go types the host writes, "+
			"so a Go wire change reaches the Rust mirror test.\nFix: run %s, then make the Rust mirrors in "+
			"lycaon-den/src-tauri pass ./task den:test:rust.\nwant:\n%s", rustHostWireFixturePath, rustHostWireRefresh, want)
	}
}

// goTypeFixture encodes one example with every JSON field set, so omitempty
// never hides a key the host can send.
func goTypeFixture(t *testing.T, source string, zero any) rustHostWireFixture {
	t.Helper()
	value := reflect.New(reflect.TypeOf(zero)).Elem()
	fillWireExample(value, "")
	example := encodeWireExample(t, source, value.Interface())
	if want := jsonFieldNames(value.Type()); !sameKeys(example, want) {
		t.Fatalf("%s example keys = %v, want every JSON field %v; teach fillWireExample the missing field kind", source, sortedKeys(example), want)
	}
	return rustHostWireFixture{Source: source, Examples: []map[string]any{example}}
}

func fillWireExample(value reflect.Value, name string) {
	if value.Type() == reflect.TypeOf(time.Time{}) {
		value.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
		return
	}
	switch value.Kind() {
	case reflect.String:
		value.SetString(name)
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(1)
	case reflect.Float32, reflect.Float64:
		value.SetFloat(1.5)
	case reflect.Pointer:
		value.Set(reflect.New(value.Type().Elem()))
		fillWireExample(value.Elem(), name)
	case reflect.Slice:
		slice := reflect.MakeSlice(value.Type(), 1, 1)
		fillWireExample(slice.Index(0), name)
		value.Set(slice)
	case reflect.Map:
		m := reflect.MakeMapWithSize(value.Type(), 1)
		key := reflect.New(value.Type().Key()).Elem()
		fillWireExample(key, name)
		item := reflect.New(value.Type().Elem()).Elem()
		if item.Kind() == reflect.Interface {
			item.Set(reflect.ValueOf(name))
		} else {
			fillWireExample(item, name)
		}
		m.SetMapIndex(key, item)
		value.Set(m)
	case reflect.Interface:
		value.Set(reflect.ValueOf(name))
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			jsonName, ok := jsonFieldName(field)
			if !ok {
				continue
			}
			fillWireExample(value.Field(i), jsonName)
		}
	default:
	}
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}
	return name, true
}

func jsonFieldNames(typ reflect.Type) []string {
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Anonymous && field.Type.Kind() == reflect.Struct && field.Tag.Get("json") == "" {
			names = append(names, jsonFieldNames(field.Type)...)
			continue
		}
		if name, ok := jsonFieldName(field); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func encodeWireExample(t *testing.T, source string, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	contractcheck.FailErr(t, "encode "+source, err)
	var example map[string]any
	contractcheck.FailErr(t, "decode "+source, json.Unmarshal(raw, &example))
	return example
}

// startupRecordFixture captures the records the engine's reporter writes to
// the desktop parent; volatile values are pinned so the fixture is stable.
func startupRecordFixture(t *testing.T) rustHostWireFixture {
	t.Helper()
	t.Setenv("LYCAON_STARTUP_PROTOCOL", "1")
	var out bytes.Buffer
	ready, err := startupprotocol.FromEnvironment(&out, nil)
	contractcheck.FailErr(t, "start startup reporter", err)
	contractcheck.FailErr(t, "report store phase", ready.Phase(startupprotocol.PhaseStore))
	contractcheck.FailErr(t, "report ready", ready.Ready(8787))
	ready.Close()
	failed, err := startupprotocol.FromEnvironment(&out, nil)
	contractcheck.FailErr(t, "start startup reporter", err)
	contractcheck.FailErr(t, "report failure", failed.Failed("store_locked"))
	failed.Close()

	var examples []map[string]any
	decoder := json.NewDecoder(&out)
	for decoder.More() {
		var record map[string]any
		contractcheck.FailErr(t, "decode startup record", decoder.Decode(&record))
		if record["kind"] == "heartbeat" {
			continue
		}
		record["pid"] = 4242
		record["elapsed_ms"] = 0
		record["sequence"] = len(examples) + 1
		examples = append(examples, record)
	}
	if len(examples) != 5 {
		t.Fatalf("startup reporter wrote %d non-heartbeat records, want launch, store, ready, launch, failed", len(examples))
	}
	// Re-encode so pinned ints match the float64 shape of decoded JSON.
	var normalized []map[string]any
	raw, err := json.Marshal(examples)
	contractcheck.FailErr(t, "encode startup records", err)
	contractcheck.FailErr(t, "decode startup records", json.Unmarshal(raw, &normalized))
	return rustHostWireFixture{Source: "internal/startupprotocol.Reporter output", Examples: normalized}
}

func sameKeys(example map[string]any, want []string) bool {
	return fmt.Sprint(sortedKeys(example)) == fmt.Sprint(want)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
