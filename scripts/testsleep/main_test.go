package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const fixture = `package fixture

import (
	"os"
	"testing"
	"testing/synctest"
	clock "time"
)

func TestFixed(t *testing.T) {
	clock.Sleep(clock.Second) // flagged: fixed
}

func TestPoll(t *testing.T) {
	for range 3 {
		clock.Sleep(clock.Millisecond)
	}
}

func TestClosureInLoop(t *testing.T) {
	for range 3 {
		go func() {
			clock.Sleep(clock.Second) // flagged: closure
		}()
	}
}

func TestBubble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock.Sleep(clock.Second)
		go func() { clock.Sleep(clock.Second) }()
	})
}

func TestHelperBranch(t *testing.T) {
	if os.Getenv("FIXTURE_HELPER") == "1" {
		clock.Sleep(clock.Minute)
	}
	switch os.Getenv("FIXTURE_ROLE") {
	case "holder":
		clock.Sleep(clock.Minute)
	}
}

func TestHelperGate(t *testing.T) {
	if os.Getenv("FIXTURE_HELPER") != "1" {
		t.Skip("helper process only")
	}
	clock.Sleep(clock.Minute)
}

func TestMain(m *testing.M) {
	clock.Sleep(clock.Second)
	os.Exit(m.Run())
}

func probe() int {
	clock.Sleep(clock.Second)
	return 0
}

func entry() {
	os.Exit(probe())
}

func ends() {
	clock.Sleep(clock.Second)
	os.Exit(1)
}

func fake() {
	clock.Sleep(clock.Second) // flagged: fake
}
`

func TestScanFlagsOnlyFixedSleeps(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture_test.go"), []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Non-test files and testdata are not the test path.
	for _, path := range []string{"fixture.go", "testdata/skipped_test.go"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(fixture), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	findings, err := scan(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	var lines []int
	for _, finding := range findings {
		var line int
		if _, err := fmt.Sscanf(finding, filepath.ToSlash(filepath.Join(root, "fixture_test.go"))+":%d:", &line); err != nil {
			t.Fatalf("unexpected finding %q", finding)
		}
		lines = append(lines, line)
	}
	if want := []int{11, 23, 72}; !slices.Equal(lines, want) {
		t.Fatalf("flagged lines %v, want %v:\n%v", lines, want, findings)
	}
}
