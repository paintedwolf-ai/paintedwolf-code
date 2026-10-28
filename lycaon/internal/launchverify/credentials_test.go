package launchverify

import "testing"

func TestAllCredentialIdentitiesMustMatch(t *testing.T) {
	for index := range 4 {
		process := Snapshot{UIDs: [4]uint32{1000, 1000, 1000, 1000}}
		if !process.matchesUID(1000) {
			t.Fatal("rejected ordinary login identity")
		}
		process.UIDs[index] = 0
		if process.matchesUID(1000) {
			t.Errorf("accepted different uid identity at index%d", index)
		}
	}
	for _, raw := range []string{"Uid: 1000 1000", "Uid: 1000 1000 1000 1000\nUid: 1000 1000 1000 1000", "Uid: 1000 -1 1000 1000", "Uid: 1000 1000 1000 4294967296"} {
		if _, err := parseStatusUIDs([]byte(raw)); err == nil {
			t.Errorf("accepted ambiguous credentials %q", raw)
		}
	}
}
