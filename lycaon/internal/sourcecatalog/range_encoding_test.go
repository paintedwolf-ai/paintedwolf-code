package sourcecatalog

import (
	"bytes"
	"math"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRangePageEncoding(t *testing.T) {
	fingerprint := pagedview.FingerprintOf([]byte("visible rows"))
	baseline := pagedview.FingerprintOf([]byte("closed rows"))
	for _, page := range []pagedview.RangePage[TreeItem]{
		{},
		{Items: []pagedview.RangeItem[TreeItem]{{Key: "0café\x00Café", Value: TreeItem{Path: "nested/日本語", Symlink: true, Sequence: math.MaxInt64}, Weight: math.MaxInt64, Unresolved: math.MaxInt64, Fingerprint: fingerprint, BaselineFingerprint: baseline}}},
		{Items: []pagedview.RangeItem[TreeItem]{{Key: "shared", Value: TreeItem{Path: "shared"}, Weight: 1, Fingerprint: fingerprint, BaselineFingerprint: fingerprint}}},
		{Items: []pagedview.RangeItem[TreeItem]{
			{Key: "a", Value: TreeItem{Path: "parent/日本語/a"}, Weight: 1},
			{Key: "b", Value: TreeItem{Path: "parent/日本語/b"}, Weight: 1},
		}},
		{Items: []pagedview.RangeItem[TreeItem]{
			{Key: "a", Value: TreeItem{Path: "left/a"}, Weight: 1},
			{Key: "b", Value: TreeItem{Path: "right/b"}, Weight: 1},
		}},
		{Children: []pagedview.Branch{{Key: "branch", Page: math.MaxUint64, Count: 500, Weight: 50_000, Unresolved: 37, Fingerprint: fingerprint, BaselineFingerprint: baseline}}},
		{Children: []pagedview.Branch{{Key: "shared", Page: 1, Count: 1, Weight: 1, Fingerprint: fingerprint, BaselineFingerprint: fingerprint}}},
	} {
		body, err := encodeRangePage(page)
		testutil.FailErr(t, "encode range page", err)
		decoded, err := decodeRangePage(body)
		testutil.FailErr(t, "decode range page", err)
		if !reflect.DeepEqual(page, decoded) {
			t.Fatalf("page changed: got %+v, want %+v", decoded, page)
		}
		for end := range len(body) {
			if _, err := decodeRangePage(body[:end]); err == nil {
				t.Fatalf("accepted truncated page at %d of %d bytes", end, len(body))
			}
		}
		if _, err := decodeRangePage(append(body, 0)); err == nil {
			t.Fatal("accepted trailing page data")
		}
	}
}

func TestRangePageEncodingSharesEqualBaselineFingerprint(t *testing.T) {
	fingerprint := pagedview.FingerprintOf([]byte("same"))
	distinct := pagedview.FingerprintOf([]byte("different"))
	shared, err := encodeRangePage(pagedview.RangePage[TreeItem]{Items: []pagedview.RangeItem[TreeItem]{{
		Key: "file", Value: TreeItem{Path: "file"}, Weight: 1, Fingerprint: fingerprint, BaselineFingerprint: fingerprint,
	}}})
	testutil.FailErr(t, "encode shared baseline", err)
	separate, err := encodeRangePage(pagedview.RangePage[TreeItem]{Items: []pagedview.RangeItem[TreeItem]{{
		Key: "file", Value: TreeItem{Path: "file"}, Weight: 1, Fingerprint: fingerprint, BaselineFingerprint: distinct,
	}}})
	testutil.FailErr(t, "encode distinct baseline", err)
	if got := len(separate) - len(shared); got != len(fingerprint) {
		t.Fatalf("shared baseline saved %d bytes, want %d", got, len(fingerprint))
	}
	decoded, err := decodeRangePage(shared)
	testutil.FailErr(t, "decode shared baseline", err)
	if decoded.Items[0].BaselineFingerprint != fingerprint {
		t.Fatal("shared baseline fingerprint was not restored")
	}

	corrupt := append([]byte(nil), shared...)
	position := bytes.Index(corrupt, fingerprint[:])
	if position < 0 || position+len(fingerprint) >= len(corrupt) {
		t.Fatal("encoded fingerprint missing")
	}
	corrupt[position+len(fingerprint)] = 0x80
	if _, err := decodeRangePage(corrupt); err == nil {
		t.Fatal("accepted unknown range measure flags")
	}
}

func FuzzRangePageEncoding(f *testing.F) {
	page := pagedview.RangePage[TreeItem]{Items: []pagedview.RangeItem[TreeItem]{{Key: "file", Value: TreeItem{Path: "src/file"}, Weight: 1}}}
	body, err := encodeRangePage(page)
	if err != nil {
		f.Fatalf("encode seed page: %v", err)
	}
	f.Add(body)
	f.Add([]byte{255, 255, 255})
	f.Fuzz(func(t *testing.T, body []byte) {
		page, err := decodeRangePage(body)
		if err != nil {
			return
		}
		encoded, err := encodeRangePage(page)
		testutil.FailErr(t, "encode decoded page", err)
		again, err := decodeRangePage(encoded)
		testutil.FailErr(t, "decode encoded page", err)
		if !reflect.DeepEqual(page, again) {
			t.Fatal("range page changed after encoding")
		}
	})
}
