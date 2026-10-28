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
	content         []byte
}

// trackedEntries reads the exact index records, including paths containing
// whitespace. Unmerged entries and submodules need a different operation.
func trackedEntries(ctx context.Context, p Prepared, root string) ([]trackedEntry, error) {
	var output bytes.Buffer
	out := &meteredOutput{ctx: ctx, w: &output, maximum: p.owners.limit("stream_bytes")}
	diagnostics, code, err := (git.CommandReader{}).ReadCommand(ctx, root, []string{"ls-files", "--stage", "-z"}, nil, out)
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
			return nil, unsupportedFileForm(p, name, "a merged index")
		}
		entries = append(entries, trackedEntry{path: name, mode: fields[0], oid: fields[1]})
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
		return nil, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "maximum": maximum, "path": entry.path}}
	}
	return content, nil
}

func entryAbs(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
