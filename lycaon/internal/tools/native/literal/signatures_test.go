package literal

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// TestFileIdentifiesTheVersionedCorpus runs every corpus fixture: complete
// headers identify, truncated headers never fabricate their format.
func TestFileIdentifiesTheVersionedCorpus(t *testing.T) {
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "catalog", err)
	var raw struct {
		FileSignatures struct {
			Fixtures []struct {
				ID          string `json:"id"`
				InputHex    string `json:"input_hex"`
				Description string `json:"description"`
				MIME        string `json:"mime"`
				Expected    string `json:"expected"`
			} `json:"fixtures"`
		} `json:"file_signatures"`
	}
	testutil.FailErr(t, "decode signatures", json.Unmarshal(catalog.Contracts["signatures"], &raw))
	owners := newHarness(t, nil).owners
	if len(raw.FileSignatures.Fixtures) < 20 {
		t.Fatalf("corpus fixtures = %d", len(raw.FileSignatures.Fixtures))
	}
	for _, fixture := range raw.FileSignatures.Fixtures {
		data, err := hex.DecodeString(fixture.InputHex)
		testutil.FailErr(t, fixture.ID, err)
		got := owners.signatures.identify(data)
		switch {
		case fixture.Description != "":
			if got.description != fixture.Description || got.mime != fixture.MIME {
				t.Errorf("%s = %+v, want %s / %s", fixture.ID, got, fixture.Description, fixture.MIME)
			}
		case strings.HasPrefix(fixture.Expected, "data/application/octet-stream"):
			want := identifyData
			if got == identifyText || got == identifyASCII {
				want = got
			}
			if got != want {
				t.Errorf("%s truncated header identified as %+v", fixture.ID, got)
			}
		default:
			t.Errorf("%s has no expectation", fixture.ID)
		}
	}
}

// TestFileStructuralSignatures checks the structural forms the corpus names.
func TestFileStructuralSignatures(t *testing.T) {
	owners := newHarness(t, nil).owners
	tar := make([]byte, 512)
	copy(tar, "member.txt")
	copy(tar[257:], "ustar\x0000")
	for i := 148; i < 156; i++ {
		tar[i] = ' '
	}
	sum := 0
	for _, b := range tar {
		sum += int(b)
	}
	copy(tar[148:], []byte(strings.Repeat("0", 6-len(itoa8(sum)))+itoa8(sum)+"\x00 "))
	pe := make([]byte, 128)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[0x3c:], 0x40)
	copy(pe[0x40:], "PE\x00\x00")
	dos := make([]byte, 128)
	copy(dos, "MZ")
	fat := make([]byte, 32)
	binary.BigEndian.PutUint32(fat, 0xcafebabe)
	binary.BigEndian.PutUint32(fat[4:], 2)
	java := append([]byte(nil), fat...)
	binary.BigEndian.PutUint32(java[4:], 0x00000034)
	mach32 := make([]byte, 32)
	binary.BigEndian.PutUint32(mach32, 0xcefaedfe)
	mach64be := make([]byte, 32)
	binary.BigEndian.PutUint32(mach64be, 0xfeedfacf)
	brokenTar := append([]byte(nil), tar...)
	brokenTar[0] = 'X'
	for name, c := range map[string]struct {
		data []byte
		want identification
	}{
		"tar":          {tar, identifyTar},
		"tar checksum": {brokenTar, identifyData},
		"pe":           {pe, identification{description: "PE executable (validated PE offset/signature required)", mime: "application/vnd.microsoft.portable-executable"}},
		"dos stub":     {dos, identifyData},
		"fat":          {fat, identifyMachOU},
		"java class":   {java, identifyData},
		"macho32":      {mach32, identifyMachO},
		"macho64 be":   {mach64be, identifyMachO64},
		"gif87":        {[]byte("GIF87a..."), identifyGIF87},
		"empty":        {nil, identifyEmpty},
		"text":         {[]byte("héllo\n"), identifyText},
		"nul text":     {[]byte("a\x00b"), identifyData},
	} {
		if got := owners.signatures.identify(c.data); got != c.want {
			t.Errorf("%s = %+v, want %+v", name, got, c.want)
		}
	}
}

func itoa8(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%8)}, out...)
		n /= 8
	}
	return string(out)
}
