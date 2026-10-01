package jsonblob

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type sample struct {
	Paths []string `json:"paths"`
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	raw, err := Marshal(sample{Paths: []string{"a", "b"}}, 2)
	testutil.FailErr(t, "Marshal", err)
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`{"v":2`)) {
		t.Fatalf("v key must be first: %s", raw)
	}
	var got sample
	testutil.FailErr(t, "Unmarshal", Unmarshal(raw, &got, 2))
	if len(got.Paths) != 2 || got.Paths[0] != "a" {
		t.Fatalf("got %+v", got)
	}
	ver, ok := Version(raw)
	if !ok || ver != 2 {
		t.Fatalf("Version = %d, %v", ver, ok)
	}
}

func TestMarshalRejectsNonObject(t *testing.T) {
	_, err := Marshal([]string{"x"}, 1)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMarshalRejectsExistingV(t *testing.T) {
	_, err := Marshal(map[string]any{"v": 1, "paths": []string{}}, 2)
	if err == nil || !strings.Contains(err.Error(), `"v"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnmarshalWindow(t *testing.T) {
	cur, err := Marshal(sample{Paths: []string{"c"}}, 3)
	testutil.FailErr(t, "Marshal current", err)
	prev, err := Marshal(sample{Paths: []string{"p"}}, 2)
	testutil.FailErr(t, "Marshal previous", err)
	older, err := Marshal(sample{Paths: []string{"o"}}, 1)
	testutil.FailErr(t, "Marshal older", err)
	newer, err := Marshal(sample{Paths: []string{"n"}}, 4)
	testutil.FailErr(t, "Marshal newer", err)

	var dst sample
	testutil.FailErr(t, "current", Unmarshal(cur, &dst, 3))
	testutil.FailErr(t, "previous", Unmarshal(prev, &dst, 3))

	if err := Unmarshal(older, &dst, 3); !errors.Is(err, ErrBlobTooOld) {
		t.Fatalf("older: %v", err)
	}
	if err := Unmarshal(newer, &dst, 3); !errors.Is(err, ErrBlobTooNew) {
		t.Fatalf("newer: %v", err)
	}
	if err := Unmarshal([]byte(`{"paths":["x"]}`), &dst, 3); !errors.Is(err, ErrBlobUnversioned) {
		t.Fatalf("unversioned: %v", err)
	}
}

func TestVersionRejectsUnversionedBlob(t *testing.T) {
	_, ok := Version([]byte(`{"paths":[]}`))
	if ok {
		t.Fatal("unversioned blob must report ok=false")
	}
	var probe map[string]json.RawMessage
	testutil.FailErr(t, "still valid JSON", json.Unmarshal([]byte(`{"paths":[]}`), &probe))
}
