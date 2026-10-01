package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func openTurnDetail(t *testing.T) *Model {
	t.Helper()
	m := newSized(t)
	m.list.Select(0) // coordinator
	enter(m)
	m.list.Select(0) // turn 1
	enter(m)
	if m.level != levelTurn {
		t.Fatalf("expected turn detail, got %v", m.level)
	}
	return m
}

func TestPipeExportStripsPromptChrome(t *testing.T) {
	m := openTurnDetail(t)
	exported := m.pipeExport()
	if strings.Contains(exported, "│") || strings.Contains(exported, "┌") || strings.Contains(exported, "━") {
		t.Fatalf("pipe export must not include box-drawing chrome\n%s", exported)
	}
	if !strings.Contains(exported, "system\ncoordinator system prompt\n") {
		t.Fatalf("expected role + body lines\n%s", exported)
	}
	if !strings.Contains(exported, "user\nTell me about this repo.\n") {
		t.Fatalf("expected user message body\n%s", exported)
	}
}

func TestCleanPipeTextStripsGutters(t *testing.T) {
	in := "┌─ user ────────\n│ hello\n│ world\n│ → tool\n│   {\n│     \"a\": 1\n│   }\n"
	got := cleanPipeText(in)
	want := "user\nhello\nworld\n→ tool\n  {\n    \"a\": 1\n  }"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPipeEmptyEnterCopiesClipboard(t *testing.T) {
	m := openTurnDetail(t)
	var got string
	orig := writeClipboard
	writeClipboard = func(text string) error {
		got = text
		return nil
	}
	t.Cleanup(func() { writeClipboard = orig })

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("|")})
	if !m.piping {
		t.Fatal("| should open the pipe prompt")
	}
	if !strings.Contains(viewText(m), "|") || !strings.Contains(viewText(m), "Enter copy") {
		t.Fatalf("pipe prompt missing from footer\n%s", viewText(m))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.piping {
		t.Fatal("Enter should leave the pipe prompt")
	}
	if got == "" {
		t.Fatal("empty pipe should write the detail buffer to the clipboard")
	}
	if strings.Contains(got, "│") || !strings.Contains(got, "coordinator system prompt") {
		t.Fatalf("clipboard should be clean turn content\n%s", got)
	}
	if !strings.Contains(m.status, "copied") {
		t.Fatalf("status should confirm copy, got %q", m.status)
	}
}

func TestPipeCommandReceivesStdin(t *testing.T) {
	m := openTurnDetail(t)
	out := filepath.Join(t.TempDir(), "out.txt")
	origClip := writeClipboard
	writeClipboard = func(string) error {
		t.Fatal("typed command should not use the clipboard")
		return nil
	}
	t.Cleanup(func() { writeClipboard = origClip })

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("|")})
	for _, r := range "cat > " + out {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("piped file: %v", err)
	}
	if !strings.Contains(string(body), "Tell me about this repo.") {
		t.Fatalf("command stdin missing detail text\n%s", body)
	}
	if !strings.Contains(m.status, "piped →") {
		t.Fatalf("status should confirm pipe, got %q", m.status)
	}
}

func TestPipeEscCancels(t *testing.T) {
	m := openTurnDetail(t)
	called := false
	orig := writeClipboard
	writeClipboard = func(string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { writeClipboard = orig })

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("|")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.piping || m.pipeCmd != "" {
		t.Fatalf("Esc should cancel pipe, piping=%v cmd=%q", m.piping, m.pipeCmd)
	}
	if called {
		t.Fatal("cancel must not write the clipboard")
	}
}

func TestPipeClipboardErrorSurfaces(t *testing.T) {
	m := openTurnDetail(t)
	orig := writeClipboard
	writeClipboard = func(string) error { return errors.New("denied") }
	t.Cleanup(func() { writeClipboard = orig })

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("|")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.status, "denied") {
		t.Fatalf("clipboard error should surface in status, got %q", m.status)
	}
}

func TestPipeShellHelper(t *testing.T) {
	shell, args := pipeShell("wc -l")
	if len(args) != 2 || args[1] != "wc -l" {
		t.Fatalf("unexpected shell args: %q %v", shell, args)
	}
}
