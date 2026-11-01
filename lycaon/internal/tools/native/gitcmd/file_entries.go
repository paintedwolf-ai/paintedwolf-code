package gitcmd

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

type trackedEntry struct {
	path, mode, oid string
	unmerged        string
}

// trackedEntries reads the exact index records, including paths containing
// whitespace. Conflict records remain scoped to their paths.
func trackedEntries(ctx context.Context, p Prepared, root string, paths []string, literal bool) ([]trackedEntry, error) {
	var output bytes.Buffer
	out := &meteredOutput{ctx: ctx, w: &output, maximum: p.owners.limit("stream_bytes")}
	args := []string{"ls-files", "--stage", "-z", "--"}
	for _, path := range paths {
		if literal {
			path = ":(literal)" + path
		}
		args = append(args, path)
	}
	diagnostics, code, err := (git.CommandReader{}).ReadCommand(ctx, root, args, nil, out)
	if err != nil {
		return nil, err
	}
	if out.err != nil {
		return nil, out.err
	}
	if code != 0 {
		return nil, fmt.Errorf("read index: %s", bytes.TrimSpace(diagnostics))
	}
	var entries []trackedEntry
	conflicts := map[string]string{}
	for _, record := range strings.Split(output.String(), "\x00") {
		if record == "" {
			continue
		}
		meta, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("malformed index record")
		}
		if fields[2] != "0" {
			conflicts[name] += meta + ";"
			continue
		}
		entries = append(entries, trackedEntry{path: name, mode: fields[0], oid: fields[1]})
	}
	for name, records := range conflicts {
		entries = append(entries, trackedEntry{path: name, unmerged: records})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, nil
}

func unsupportedFileForm(p Prepared, word, expected string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{"command": p.command.spec.Name, "word": word, "expected": expected, "expected_form": p.command.spec.Argv}}
}

func fileOperands(ctx context.Context, p Prepared, dir, root string) ([]string, error) {
	operands := p.Argv.Operands().Values()
	for _, operand := range operands {
		if strings.HasPrefix(operand, ":") {
			return nil, unsupportedFileForm(p, operand, "repository paths without pathspec magic")
		}
	}
	return repoRelPaths(ctx, p, dir, root, operands)
}

func entryContent(ctx context.Context, p Prepared, root string, entry trackedEntry) ([]byte, error) {
	maximum := p.owners.limit("stream_bytes")
	content, ok, err := p.owners.Git.BlobContent(ctx, root, entry.oid, int(maximum))
	if err != nil {
		return nil, err
	}
	if !ok {
		observed, err := p.owners.Git.BlobSize(ctx, root, entry.oid)
		if err != nil {
			return nil, err
		}
		if observed <= maximum {
			return nil, fmt.Errorf("git blob %s unavailable after bounded read", entry.oid)
		}
		return nil, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": maximum, "observed": observed, "path": entry.path}}
	}
	return content, nil
}

func entryAbs(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
