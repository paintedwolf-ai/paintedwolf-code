package llm

import (
	"testing"
)

func TestMockEnabled(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")

	if MockEnabled(nil) {
		t.Fatal("expected mock disabled without test client or env")
	}
	if !MockEnabled(NewMockProvider(nil)) {
		t.Fatal("expected mock enabled when test client injected")
	}
	t.Setenv("LYCAON_LLM_MOCK", "1")
	if !MockEnabled(nil) {
		t.Fatal("expected mock enabled when LYCAON_LLM_MOCK=1")
	}
}

func TestMockOnlyFromEnv(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	if MockOnlyFromEnv() {
		t.Fatal("expected false when unset")
	}
	t.Setenv("LYCAON_LLM_MOCK", "1")
	if !MockOnlyFromEnv() {
		t.Fatal("expected true for 1")
	}
	t.Setenv("LYCAON_LLM_MOCK", "true")
	if !MockOnlyFromEnv() {
		t.Fatal("expected true for true")
	}
	t.Setenv("LYCAON_LLM_MOCK", "0")
	if MockOnlyFromEnv() {
		t.Fatal("expected false for 0")
	}
}

func TestProviderUtilityCallsEnabled(t *testing.T) {
	t.Setenv("LYCAON_DEV", "1")
	t.Setenv("LYCAON_LLM_MOCK", "")
	t.Setenv("LYCAON_LLM_MANUAL", "")
	if !ProviderUtilityCallsEnabled() {
		t.Fatal("provider utility calls disabled outside test LLM modes")
	}
	t.Setenv("LYCAON_LLM_MANUAL", "1")
	if ProviderUtilityCallsEnabled() {
		t.Fatal("provider utility calls enabled in manual harness mode")
	}
	t.Setenv("LYCAON_LLM_MANUAL", "")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	if ProviderUtilityCallsEnabled() {
		t.Fatal("provider utility calls enabled in mock mode")
	}
}
