package use

import "ident/owner"

const local = "local"

func compare(r owner.Ref) bool {
	if r.ID == "" || r.ID == owner.Single {
		return true
	}
	if r.Name == "anything" {
		return true
	}
	switch r.ID {
	case owner.Single:
	case "legacy": // want `root identity ident/owner.Ref.ID spelled as "legacy"`
	}
	return r.ID == "primary" || local == r.ID // want `spelled as "primary"` `spelled as "local"`
}

func build(id string) owner.Ref {
	r := owner.Ref{ID: "fixed"} // want `spelled as "fixed"`
	r.ID = owner.Single
	r.ID = id
	if id == "" {
		r.ID = "fallback" // want `spelled as "fallback"`
	}
	return owner.Ref{ID: id, Name: "plain"}
}
