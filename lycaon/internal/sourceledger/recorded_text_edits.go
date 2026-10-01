package sourceledger

import (
	"context"
	"errors"
	"unicode/utf16"

	"github.com/lycaon/lycaon/internal/documentcore"
)

// Surviving character identities determine the alignment of collaborative
// versions. A textual diff may align an unrelated repeated character instead.
func (s *Store) recordedTextEdits(ctx context.Context, beforeID, afterID, beforeText, afterText string) ([]documentcore.Edit, error) {
	before, err := s.versionTextState(ctx, beforeID)
	if err != nil {
		return nil, err
	}
	after, err := s.versionTextState(ctx, afterID)
	if err != nil {
		return nil, err
	}
	if before == nil || after == nil || before.DocumentID != after.DocumentID || before.Epoch != after.Epoch {
		return documentcore.TextEdits(beforeText, afterText), nil
	}
	removed := changedTextSpans(before.Spans, after.Spans)
	added := changedTextSpans(after.Spans, before.Spans)
	edits := make([]documentcore.Edit, 0, len(removed)+len(added))
	for i := len(removed) - 1; i >= 0; i-- {
		span := removed[i]
		edits = append(edits, documentcore.Edit{Index: span.Index, Delete: span.Length})
	}
	text := utf16.Encode([]rune(afterText))
	for _, span := range added {
		if uint64(span.Index)+uint64(span.Length) > uint64(len(text)) {
			return nil, errors.New("saved character identities exceed their text version")
		}
		edits = append(edits, documentcore.Edit{Index: span.Index, Insert: string(utf16.Decode(text[span.Index : span.Index+span.Length]))})
	}
	return edits, nil
}
