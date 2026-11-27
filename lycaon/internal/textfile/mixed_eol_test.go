package textfile

import "testing"

func TestSerializePreservingMixedEOL(t *testing.T) {
	cases := []struct {
		name      string
		beforeRaw string
		draftLF   string
		fallback  string
		want      string
		wantMixed bool
	}{
		{
			name:      "middle_line_edit_preserves_outside_crlf_and_lf",
			beforeRaw: "L1_crlf\r\nTARGET\nL3_lf\n",
			draftLF:   "L1_crlf\nEDITED\nL3_lf\n",
			fallback:  "lf",
			want:      "L1_crlf\r\nEDITED\nL3_lf\n",
			wantMixed: true,
		},
		{
			name:      "multi_hunk_preserves_untouched_interior_crlf",
			beforeRaw: "A\r\nB\nC\r\nD\nE\r\n",
			draftLF:   "A\nB_new\nC\nD_new\nE\n",
			fallback:  "lf",
			want:      "A\r\nB_new\nC\r\nD_new\nE\r\n",
			wantMixed: true,
		},
		{
			name:      "inserted_line_uses_fallback_crlf",
			beforeRaw: "L1\r\nL2\nL3\r\n",
			draftLF:   "L1\nINSERTED\nL2\nL3\n",
			fallback:  "crlf",
			want:      "L1\r\nINSERTED\r\nL2\nL3\r\n",
			wantMixed: true,
		},
		{
			name:      "uniform_before_uses_fallback",
			beforeRaw: "L1\nL2\nL3\n",
			draftLF:   "L1\nEDITED\nL3\n",
			fallback:  "crlf",
			want:      "L1\r\nEDITED\r\nL3\r\n",
			wantMixed: false,
		},
		{
			name:      "preserves_no_trailing_newline",
			beforeRaw: "L1_crlf\r\nTARGET\nL3_eof",
			draftLF:   "L1_crlf\nEDITED\nL3_eof",
			fallback:  "lf",
			want:      "L1_crlf\r\nEDITED\nL3_eof",
			wantMixed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, mixed := SerializePreservingMixedEOL(tc.beforeRaw, tc.draftLF, tc.fallback)
			if got != tc.want || mixed != tc.wantMixed {
				t.Fatalf("SerializePreservingMixedEOL() = (%q, %v), want (%q, %v)", got, mixed, tc.want, tc.wantMixed)
			}
		})
	}
}
