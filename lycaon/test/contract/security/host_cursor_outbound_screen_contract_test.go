package contract

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"

	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

type codeScansPosition struct {
	WatermarkOrdinal int64  `json:"watermark_ordinal"`
	Value            string `json:"value"`
	CreatedAt        string `json:"created_at"`
	ID               string `json:"id"`
}

type summarizePosition struct {
	Revision              uint64 `json:"r"`
	Scope                 string `json:"s"`
	Position              string `json:"p"`
	MatchesObserved       int    `json:"m,omitempty"`
	MatchingFilesObserved int    `json:"f,omitempty"`
}

type directoryMapPosition struct {
	Scope    string `json:"s"`
	Revision uint64 `json:"r"`
	Page     string `json:"p"`
}

type genericCursorPosition struct {
	Token string `json:"token"`
	Index int    `json:"index"`
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

func randomInt(t *testing.T, max int64) int64 {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		t.Fatalf("rand.Int: %v", err)
	}
	return n.Int64()
}

func TestHostCursorsZeroOutboundSecretMatches(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)

	const tokensPerKind = 2000

	codeScansCodec := pagecursor.For[codeScansPosition]("code_scans")
	for i := 0; i < tokensPerKind; i++ {
		scope := pagecursor.Scope(randomHex(t, 8), randomHex(t, 4))
		pos := codeScansPosition{
			WatermarkOrdinal: randomInt(t, 100000),
			Value:            randomHex(t, 12),
			CreatedAt:        "2026-10-04T05:00:00Z",
			ID:               randomHex(t, 8),
		}
		var token string
		if i%2 == 0 {
			token, err = codeScansCodec.EncodeAt(scope, uint64(randomInt(t, 500)+1), pos)
		} else {
			token, err = codeScansCodec.Encode(scope, pos)
		}
		testutil.FailErr(t, "encode code_scans cursor", err)
		payload := fmt.Sprintf(`{"next_cursor":%q}`, token)
		if matches := matcher.Screen(payload); len(matches) > 0 {
			t.Fatalf("code_scans token %q matched outbound secret rule %s", token, matches[0].RuleID)
		}
	}

	summarizeCodec := pagecursor.For[summarizePosition]("summarize")
	for i := 0; i < tokensPerKind; i++ {
		scope := pagecursor.Scope(randomHex(t, 8))
		pos := summarizePosition{
			Revision:              uint64(randomInt(t, 100) + 1),
			Scope:                 scope,
			Position:              fmt.Sprintf("src/path_%s.go", randomHex(t, 4)),
			MatchesObserved:       int(randomInt(t, 50)),
			MatchingFilesObserved: int(randomInt(t, 20)),
		}
		token, err := summarizeCodec.Encode(scope, pos)
		testutil.FailErr(t, "encode summarize cursor", err)
		payload := fmt.Sprintf(`{"next_cursor":%q}`, token)
		if matches := matcher.Screen(payload); len(matches) > 0 {
			t.Fatalf("summarize token %q matched outbound secret rule %s", token, matches[0].RuleID)
		}
	}

	directoryMapCodec := pagecursor.For[directoryMapPosition]("directory_map")
	for i := 0; i < tokensPerKind; i++ {
		scope := pagecursor.Scope(randomHex(t, 6))
		pos := directoryMapPosition{
			Scope:    scope,
			Revision: uint64(randomInt(t, 50) + 1),
			Page:     fmt.Sprintf("page_%s", randomHex(t, 4)),
		}
		token, err := directoryMapCodec.Encode(scope, pos)
		testutil.FailErr(t, "encode directory_map cursor", err)
		payload := fmt.Sprintf(`{"next_cursor":%q}`, token)
		if matches := matcher.Screen(payload); len(matches) > 0 {
			t.Fatalf("directory_map token %q matched outbound secret rule %s", token, matches[0].RuleID)
		}
	}

	genericCodec := pagecursor.For[genericCursorPosition]("generic")
	for i := 0; i < tokensPerKind; i++ {
		scope := pagecursor.Scope(randomHex(t, 4), randomHex(t, 4))
		pos := genericCursorPosition{
			Token: randomHex(t, 16),
			Index: int(randomInt(t, 1000)),
		}
		var token string
		if i%2 == 0 {
			token, err = genericCodec.EncodeAt(scope, uint64(randomInt(t, 200)+1), pos)
		} else {
			token, err = genericCodec.Encode(scope, pos)
		}
		testutil.FailErr(t, "encode generic cursor", err)
		payload := fmt.Sprintf(`{"next_cursor":%q}`, token)
		if matches := matcher.Screen(payload); len(matches) > 0 {
			t.Fatalf("generic token %q matched outbound secret rule %s", token, matches[0].RuleID)
		}
	}
}
