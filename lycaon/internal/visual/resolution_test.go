package visual

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolutionTotality(t *testing.T) {
	present := Present(api.VisualArtifact{ID: "art-1", Mime: "image/png"}, []byte("bytes"))
	if !present.IsPresent() || len(present.Bytes()) == 0 {
		t.Fatalf("present resolution = %+v", present)
	}
	if !present.Meta().StoreRef {
		t.Fatal("a present resolution must carry a store-ref wire artifact")
	}
	if present.Reason() != "" || present.Note() != "" {
		t.Fatalf("present carries an absence reason: %q / %q", present.Reason(), present.Note())
	}

	seen := map[string]bool{}
	for _, reason := range AbsenceReasons() {
		res := Absent(reason)
		if res.IsPresent() {
			t.Fatalf("Absent(%s) reports present", reason)
		}
		if res.Bytes() != nil {
			t.Fatalf("Absent(%s) carries bytes", reason)
		}
		if res.Reason() != reason {
			t.Fatalf("Absent(%s).Reason() = %q", reason, res.Reason())
		}
		note := strings.TrimSpace(res.Note())
		if note == "" {
			t.Fatalf("Absent(%s) has no sentence for a person to read", reason)
		}
		if seen[note] {
			t.Fatalf("Absent(%s) reuses another reason's copy: %q", reason, note)
		}
		seen[note] = true
	}

	// Unknown reasons normalize to presentable copy.
	if got := Absent("wat").Reason(); got != AbsenceUnknown {
		t.Fatalf("unknown reason normalized to %q want %q", got, AbsenceUnknown)
	}
	var zero Resolution
	if got := zero.Reason(); got != AbsenceUnknown {
		t.Fatalf("zero Resolution reason = %q want %q", got, AbsenceUnknown)
	}
	if zero.IsPresent() {
		t.Fatal("the zero Resolution must not read as present")
	}
}

func TestPresentWithoutBytesIsUnavailable(t *testing.T) {
	res := Present(api.VisualArtifact{ID: "art-1", Mime: "image/png"}, nil)
	if res.IsPresent() {
		t.Fatal("Present(nil bytes) reported present")
	}
	if res.Reason() != AbsenceUnavailable {
		t.Fatalf("reason = %q want %q", res.Reason(), AbsenceUnavailable)
	}
}
