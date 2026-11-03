package secretmatch

import "context"

// StreamChangedError refuses publication under a changed evidence revision.
type StreamChangedError struct{ Resource string }

func (e *StreamChangedError) Error() string { return e.Resource + " changed during stream screening" }

func (m *Matcher) streamClassifications(ctx context.Context, memory *streamMemory) (context.Context, string, func() error, error) {
	source := m.classifications(ctx)
	bytes := int64(len(source))*256 + 128
	for fp := range source {
		if err := ctx.Err(); err != nil {
			return nil, "", nil, err
		}
		bytes += int64(len(fp)) * 3
	}
	if err := memory.take(bytes); err != nil {
		return nil, "", nil, err
	}
	snapshot := make(map[SecretFingerprint]bool, len(source))
	active := make([]SecretFingerprint, 0, len(source))
	for fp, v := range source {
		snapshot[fp] = v
		if v {
			active = append(active, fp)
		}
	}
	revision := ""
	if m.ignoredSource != nil {
		revision = FingerprintDigest(active)
	}
	check := func() error {
		var current map[SecretFingerprint]bool
		if m.ignoredSource != nil {
			current = m.ignoredSource(ctx)
		}
		if len(current) != len(snapshot) {
			return &StreamChangedError{"classifications"}
		}
		for fp, v := range snapshot {
			if got, ok := current[fp]; !ok || got != v {
				return &StreamChangedError{"classifications"}
			}
		}
		return nil
	}
	return m.WithClassifications(ctx, snapshot), revision, check, nil
}
