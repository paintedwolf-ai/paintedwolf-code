package captureprojection

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func TestScreenStreamManagedEchoRechecksStandingOnLaterPublication(t *testing.T) {
	const value = "managed-capture-value-9917"
	reference := secretmatch.ReferenceToken("12345678-1234-1234-1234-123456789abc")
	generation := uint64(1)
	retired := false
	matcher := secretmatch.NewInertMatcher()
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		ref := reference
		if retired {
			ref = ""
		}
		return []secretmatch.HarvestedValue{{Secret: value, RuleID: secretmatch.ManagedRuleID, Reference: ref, Retired: retired, NonDisclosable: true}}
	})
	primes := 0
	projector := New(matcher, func(context.Context, string, string) error { primes++; return nil })
	projector.SetManagedSecretGeneration(func() uint64 { return generation })
	encoded := base64.StdEncoding.EncodeToString([]byte("user:" + value))
	raw := value + "\n" + encoded
	for _, later := range []bool{false, true} {
		retired = later
		generation++
		memory := &streamMemoryFixture{}
		options := streamOptionsFixture(memory)
		options.Policy = secretmatch.StreamPolicy{ManagedEchoes: true}
		var out bytes.Buffer
		metadata, err := projector.ScreenStream(t.Context(), Scope{ProjectID: "project", RootSessionID: "chat", SessionID: "worker"}, "capture.native", strings.NewReader(raw), &out, options)
		if err != nil || !metadata.Complete || !metadata.Redacted || memory.outstanding != 0 {
			t.Fatalf("managed publication: %+v %v memory=%d", metadata, err, memory.outstanding)
		}
		if strings.Contains(out.String(), value) || strings.Contains(out.String(), encoded) {
			t.Fatal("managed bytes escaped capture")
		}
		if strings.Contains(out.String(), reference) == later {
			t.Fatalf("later=%v reference standing wrong: %q", later, out.String())
		}
	}
	if primes != 2 {
		t.Fatalf("generation did not refresh evidence: %d", primes)
	}
}

func TestScreenStreamManagedEchoRefusesChangedOrCanceledPublication(t *testing.T) {
	const value = "managed-capture-value-9917"
	for _, cancel := range []bool{false, true} {
		matcher := secretmatch.NewInertMatcher()
		matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
			return []secretmatch.HarvestedValue{{Secret: value, RuleID: secretmatch.ManagedRuleID, NonDisclosable: true}}
		})
		projector := New(matcher, nil)
		generation := uint64(1)
		projector.SetManagedSecretGeneration(func() uint64 { return generation })
		ctx, stop := context.WithCancel(t.Context())
		memory := &streamMemoryFixture{}
		options := streamOptionsFixture(memory)
		options.Policy = secretmatch.StreamPolicy{ManagedEchoes: true}
		var out bytes.Buffer
		writer := revisionWriter{target: &out, change: func() {
			if cancel {
				stop()
			} else {
				generation++
			}
		}}
		raw := base64.StdEncoding.EncodeToString([]byte("user:" + value))
		metadata, err := projector.ScreenStream(ctx, Scope{}, "", strings.NewReader(raw), writer, options)
		stop()
		if err == nil || metadata.Complete || memory.outstanding != 0 || strings.Contains(out.String(), raw) {
			t.Fatalf("changed capture completed or leaked: %+v %v memory=%d", metadata, err, memory.outstanding)
		}
		var changed *secretmatch.StreamChangedError
		if cancel && !errors.Is(err, context.Canceled) || !cancel && !errors.As(err, &changed) {
			t.Fatalf("wrong change error: %v", err)
		}
	}
}
