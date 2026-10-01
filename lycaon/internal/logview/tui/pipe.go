package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	logview "github.com/lycaon/lycaon/internal/logview"
)

// writeClipboard is the pasteboard sink for an empty `|` pipe. Tests replace it.
var writeClipboard = clipboard.WriteAll

type clearStatusMsg struct{ gen int }

// pipeExport returns paste-friendly plain text for the current detail. Prompt turns
// use role/body export (no │ gutters); other panes strip box-drawing from a
// no-wrap render.
func (m *Model) pipeExport() string {
	text := m.pipeExportRaw()
	if text == "" {
		return ""
	}
	return strings.TrimRight(text, "\n") + "\n"
}

func (m *Model) pipeExportRaw() string {
	if m.level == levelTurn && !m.diffMode && m.agent != nil && m.turnIdx < len(m.agent.Turns) {
		return logview.ExportPromptPlain(m.plain, m.agent.Turns[m.turnIdx], logview.PromptOptions{
			HideSystem: !m.showSystem,
		})
	}
	return cleanPipeText(m.renderDetail(m.plain.WithWidth(0)))
}

// cleanPipeText drops box-drawing chrome from a painted detail render so paste
// gets content lines without │ / ┌─ prefixes.
func cleanPipeText(s string) string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(ln, "│"):
			rest := strings.TrimPrefix(ln, "│")
			out = append(out, strings.TrimPrefix(rest, " "))
		case strings.HasPrefix(ln, "┌─ "):
			header := strings.TrimPrefix(ln, "┌─ ")
			if i := strings.IndexAny(header, "─━"); i >= 0 {
				header = strings.TrimSpace(header[:i])
			}
			if header != "" {
				if len(out) > 0 {
					out = append(out, "")
				}
				out = append(out, header)
			}
		case ln != "" && strings.Trim(ln, "━─") == "":
			// decorative rule
		default:
			out = append(out, ln)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func (m *Model) startPipe() {
	m.piping = true
	m.pipeCmd = ""
	m.status = ""
}

func (m *Model) clearPipe() {
	m.piping = false
	m.pipeCmd = ""
}

func (m *Model) handlePipeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		cmd := strings.TrimSpace(m.pipeCmd)
		m.clearPipe()
		next := m.runPipe(cmd)
		return m, next
	case "esc":
		m.clearPipe()
		return m, nil
	case "backspace":
		if m.pipeCmd != "" {
			m.pipeCmd = m.pipeCmd[:len(m.pipeCmd)-1]
		}
		return m, nil
	default:
		if len(msg.Runes) > 0 {
			m.pipeCmd += string(msg.Runes)
		}
		return m, nil
	}
}

// runPipe sends the detail buffer to the clipboard (empty cmd) or to `$SHELL -c cmd`.
func (m *Model) runPipe(cmd string) tea.Cmd {
	text := m.pipeExport()
	lines := strings.Count(text, "\n")
	if strings.TrimSpace(text) == "" {
		return m.flashStatus("nothing to pipe")
	}
	if cmd == "" {
		if err := writeClipboard(text); err != nil {
			return m.flashStatus("clipboard: " + err.Error())
		}
		return m.flashStatus(fmt.Sprintf("copied %d lines", lines))
	}
	out, err := runPipeCommand(text, cmd)
	if err != nil {
		msg := err.Error()
		if out = strings.TrimSpace(out); out != "" {
			msg = clip(out, 60) + " · " + msg
		}
		return m.flashStatus(msg)
	}
	if out = strings.TrimSpace(out); out != "" {
		return m.flashStatus(clip(strings.Join(strings.Fields(out), " "), 72))
	}
	return m.flashStatus("piped → " + cmd)
}

func (m *Model) flashStatus(text string) tea.Cmd {
	m.statusGen++
	gen := m.statusGen
	m.status = text
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return clearStatusMsg{gen: gen}
	})
}

func runPipeCommand(text, cmdline string) (stdout string, err error) {
	shell, args := pipeShell(cmdline)
	c, cleanup, err := execpkg.PrepareCommand(context.Background(), shell, args, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("logview_pipe"),
	})
	if err != nil {
		return "", err
	}
	defer cleanup()
	c.Stdin = strings.NewReader(text)
	var stdoutBuf, stderrBuf bytes.Buffer
	c.Stdout = &stdoutBuf
	c.Stderr = &stderrBuf
	err = c.Run()
	out := stdoutBuf.String()
	if err != nil {
		if s := strings.TrimSpace(stderrBuf.String()); s != "" {
			return s, err
		}
		return out, err
	}
	return out, nil
}

func pipeShell(cmdline string) (string, []string) {
	if runtime.GOOS == "windows" {
		comspec := os.Getenv("ComSpec")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		return comspec, []string{"/C", cmdline}
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell, []string{"-c", cmdline}
}
