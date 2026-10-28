package launchverify

import "errors"

// NamespaceFacts are kernel observations, never application assertions.
type NamespaceFacts struct {
	Device          uint64
	Inode           uint64
	OwnerUID        uint32
	FullUIDMap      bool
	FullGIDMap      bool
	NoNewPrivileges bool
	Capabilities    [4]uint64
}

func sameNamespace(a, b NamespaceFacts) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.Inode != 0
}

// ProtectedNamespacePair rejects ordinary caller-created namespaces and
// privilege-bearing descendants, even if their running images hash correctly.
func ProtectedNamespacePair(initial, engine, desktop NamespaceFacts) error {
	if !sameNamespace(engine, desktop) || sameNamespace(initial, engine) {
		return errors.New("processes do not share a distinct protected user namespace")
	}
	for _, facts := range []NamespaceFacts{engine, desktop} {
		if facts.OwnerUID != 0 || !facts.FullUIDMap || !facts.FullGIDMap || !facts.NoNewPrivileges || facts.Capabilities != [4]uint64{} {
			return errors.New("protected namespace credentials are not sealed")
		}
	}
	return nil
}

func (v Verifier) protectedNamespace(engine, desktop int) (NamespaceFacts, error) {
	read := v.NamespaceReader
	if read == nil {
		read = ReadNamespace
	}
	initial, err := read(v.ProcRoot, 0)
	if err != nil {
		return NamespaceFacts{}, refuse(CodeNamespaceInvalid, RoleEngine, engine, err.Error())
	}
	actual, err := read(v.ProcRoot, engine)
	if err != nil {
		return NamespaceFacts{}, refuse(CodeNamespaceInvalid, RoleEngine, engine, err.Error())
	}
	parent, err := read(v.ProcRoot, desktop)
	if err != nil {
		return NamespaceFacts{}, refuse(CodeNamespaceInvalid, RoleDesktopApp, desktop, err.Error())
	}
	if err := ProtectedNamespacePair(initial, actual, parent); err != nil {
		return NamespaceFacts{}, refuse(CodeNamespaceInvalid, RoleEngine, engine, err.Error())
	}
	return actual, nil
}

func (v Verifier) unchangedNamespace(chain Chain) error {
	actual, err := v.protectedNamespace(chain.Engine.PID, chain.Launcher.PID)
	if err != nil {
		return err
	}
	if actual != chain.Namespace {
		return refuse(CodeNamespaceInvalid, RoleEngine, chain.Engine.PID, "protected namespace changed")
	}
	return nil
}
