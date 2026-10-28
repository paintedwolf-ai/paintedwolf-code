package literal

import "testing"

func TestMountinfoEscapesDecode(t *testing.T) {
	for field, want := range map[string]string{`/mnt/a\040b`: "/mnt/a b", `/x\134y`: `/x\y`, `/plain`: "/plain", `/end\04`: `/end\04`} {
		if got := unescapeMount(field); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", field, got, want)
		}
	}
}
