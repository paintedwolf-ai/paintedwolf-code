package sourcecomparison

import (
	"context"
	"encoding/binary"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/pkg/api"
	"math"
	"sort"
)

// Complete-text lexing preserves multiline context. The retained index stores
// delta-encoded coordinates and one byte per token kind, with bounded seek blocks.
type syntaxBlock struct {
	offset    uint32
	base, end uint32
}
type syntaxIndex struct {
	data   []byte
	blocks []syntaxBlock
}

const syntaxBlockTokens = 128

var syntaxKinds = [...]string{"", "type", "keyword", "string", "number", "comment", "function", "operator", "property", "constant", "variable"}

func syntaxSpans(ctx context.Context, path, text string) (syntaxIndex, error) {
	if err := ctx.Err(); err != nil {
		return syntaxIndex{}, err
	}
	lexer := lexers.Match(path)
	if lexer == nil {
		return syntaxIndex{}, nil
	}
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	if err != nil {
		return syntaxIndex{}, nil //nolint:nilerr // Lexer failure omits decoration; retained text remains readable.
	}
	var out syntaxIndex
	var at, previous uint64
	count := 0
	for token := iterator(); token != chroma.EOF; token = iterator() {
		if err := ctx.Err(); err != nil {
			return syntaxIndex{}, err
		}
		tokenWidth := width(token.Value)
		if tokenWidth < 0 {
			return syntaxIndex{}, pagedview.ErrRange
		}
		end := at + uint64(tokenWidth)
		offset := len(out.data)
		if end > math.MaxUint32 || at > math.MaxUint32 || offset > math.MaxUint32 {
			return syntaxIndex{}, pagedview.ErrBudget
		}
		if kind := syntaxKindID(token.Type); kind != 0 && end > at {
			if count%syntaxBlockTokens == 0 {
				out.blocks = append(out.blocks, syntaxBlock{offset: uint32(offset), base: uint32(at)})
				previous = at
			}
			out.data = append(out.data, kind)
			out.data = binary.AppendUvarint(out.data, at-previous)
			out.data = binary.AppendUvarint(out.data, end-at)
			out.blocks[len(out.blocks)-1].end = uint32(end)
			previous = end
			count++
		}
		at = end
	}
	return out, nil
}

func syntaxKindID(kind chroma.TokenType) byte {
	name := syntaxKind(kind)
	for i, value := range syntaxKinds {
		if name == value {
			return byte(i)
		}
	}
	return 0
}

func syntaxKind(token chroma.TokenType) string {
	switch {
	case token == chroma.KeywordType || token == chroma.NameClass:
		return "type"
	case token.InCategory(chroma.Keyword):
		return "keyword"
	case token.InSubCategory(chroma.LiteralString):
		return "string"
	case token.InSubCategory(chroma.LiteralNumber):
		return "number"
	case token.InCategory(chroma.Comment):
		return "comment"
	case token.InSubCategory(chroma.NameFunction):
		return "function"
	case token.InCategory(chroma.Operator):
		return "operator"
	case token == chroma.NameAttribute || token == chroma.NameProperty:
		return "property"
	case token == chroma.NameConstant:
		return "constant"
	case token.InCategory(chroma.Name):
		return "variable"
	default:
		return ""
	}
}

func rowSyntax(index syntaxIndex, from, length int) []api.SourceReaderToken {
	var out []api.SourceReaderToken
	if from < 0 || length < 0 || length > math.MaxInt-from {
		return nil
	}
	first := sort.Search(len(index.blocks), func(i int) bool { return int(index.blocks[i].end) > from })
	for blockAt := first; blockAt < len(index.blocks); blockAt++ {
		block := index.blocks[blockAt]
		if int(block.base) >= from+length {
			break
		}
		end := len(index.data)
		if blockAt+1 < len(index.blocks) {
			end = int(index.blocks[blockAt+1].offset)
		}
		data, previous := index.data[block.offset:end], int(block.base)
		for len(data) > 0 {
			kind := data[0]
			gap, n := binary.Uvarint(data[1:])
			if n <= 0 || gap > math.MaxInt {
				return out
			}
			if int(gap) > math.MaxInt-previous {
				return out
			}
			start := previous + int(gap)
			width, m := binary.Uvarint(data[1+n:])
			if m <= 0 || width > math.MaxInt {
				return out
			}
			if int(width) > math.MaxInt-start {
				return out
			}
			data = data[1+n+m:]
			previous = start + int(width)
			if start >= from+length {
				return out
			}
			if previous > from {
				out = append(out, api.SourceReaderToken{From: max(0, start-from), To: min(length, previous-from), Kind: syntaxKinds[kind]})
			}
		}
	}
	return out
}
