package mutation

import (
	"fmt"
	"io"
	"strings"
)

// A single hunk bounds preview work even when every line changes.
func (inv *invocation) previewRewrite(path, before, after string) error {
	lines := strings.Count(before, "\n") + strings.Count(after, "\n") + 2
	release, err := inv.owners.Revisions.Memory.Reserve(inv.ctx, int64(lines)*16)
	if err != nil {
		return err
	}
	defer release()
	oldLines, newLines := previewLines(before), previewLines(after)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix && oldLines[len(oldLines)-suffix-1] == newLines[len(newLines)-suffix-1] {
		suffix++
	}
	start := max(0, prefix-3)
	oldEnd, newEnd := min(len(oldLines), len(oldLines)-suffix+3), min(len(newLines), len(newLines)-suffix+3)
	oldStart, newStart := start+1, start+1
	if oldEnd == start {
		oldStart = start
	}
	if newEnd == start {
		newStart = start
	}
	if _, err := fmt.Fprintf(inv.stdout, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", path, path, oldStart, oldEnd-start, newStart, newEnd-start); err != nil {
		return err
	}
	for _, part := range []struct {
		tag   string
		lines []string
	}{
		{" ", oldLines[start:prefix]},
		{"-", oldLines[prefix : len(oldLines)-suffix]},
		{"+", newLines[prefix : len(newLines)-suffix]},
		{" ", oldLines[len(oldLines)-suffix : oldEnd]},
	} {
		for _, line := range part.lines {
			if err := inv.ctx.Err(); err != nil {
				return err
			}
			if _, err := io.WriteString(inv.stdout, part.tag); err != nil {
				return err
			}
			if _, err := io.WriteString(inv.stdout, line); err != nil {
				return err
			}
			if !strings.HasSuffix(line, "\n") {
				if _, err := io.WriteString(inv.stdout, "\n\\ No newline at end of file\n"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func previewLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
