package definition

import (
	"testing"
)

func TestHashBlueprintContentStable(t *testing.T) {
	md := "# Spec\n\nBody.\n"
	h1 := HashBlueprintContent(md)
	h2 := HashBlueprintContent(md)
	if h1 == "" || h1 != h2 {
		t.Fatalf("hash = %q want stable non-empty", h1)
	}
	if HumanApprovalContentMatches(h1, md) != true {
		t.Fatal("content should match stored hash")
	}
	if HumanApprovalContentMatches(h1, md+"\nedit") != false {
		t.Fatal("edited content must not match")
	}
}
