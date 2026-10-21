package launchverify

import "testing"

func TestProtectedNamespacePair(t *testing.T) {
	initial := NamespaceFacts{Device: 1, Inode: 10}
	protected := NamespaceFacts{Device: 1, Inode: 20, FullUIDMap: true, FullGIDMap: true, NoNewPrivileges: true}
	if err := ProtectedNamespacePair(initial, protected, protected); err != nil {
		t.Fatalf("accept sealed root-created namespace: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*NamespaceFacts)
	}{
		{"original namespace", func(f *NamespaceFacts) { f.Inode = 10 }},
		{"ordinary user owner", func(f *NamespaceFacts) { f.OwnerUID = 1000 }},
		{"remapped UID", func(f *NamespaceFacts) { f.FullUIDMap = false }},
		{"remapped GID", func(f *NamespaceFacts) { f.FullGIDMap = false }},
		{"unsealed exec", func(f *NamespaceFacts) { f.NoNewPrivileges = false }},
		{"permitted capability", func(f *NamespaceFacts) { f.Capabilities[0] = 1 }},
		{"effective capability", func(f *NamespaceFacts) { f.Capabilities[1] = 1 }},
		{"inheritable capability", func(f *NamespaceFacts) { f.Capabilities[2] = 1 }},
		{"ambient capability", func(f *NamespaceFacts) { f.Capabilities[3] = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := protected
			test.mutate(&changed)
			if err := ProtectedNamespacePair(initial, changed, changed); err == nil {
				t.Fatal("accepted an unprotected namespace")
			}
		})
	}
	different := protected
	different.Inode++
	if err := ProtectedNamespacePair(initial, protected, different); err == nil {
		t.Fatal("accepted unrelated root-created namespace roles")
	}
}

func TestChainRecheckRejectsAChangedNamespace(t *testing.T) {
	_, verifier := chainFixture(t)
	chain, err := verifier.VerifyChain(301, 1000)
	if err != nil {
		t.Fatalf("verify sealed chain: %v", err)
	}
	verifier.NamespaceReader = func(root string, pid int) (NamespaceFacts, error) {
		facts, err := fixtureNamespace(root, pid)
		if pid != 0 {
			facts.Inode++
		}
		return facts, err
	}
	if err := verifier.Recheck(chain); CodeOf(err) != CodeNamespaceInvalid {
		t.Fatalf("changed namespace refusal: %v", err)
	}
}
