package api

import "testing"

func TestSessionIsWorkerChild(t *testing.T) {
	if (&Session{ParentSessionID: "parent-1"}).IsWorkerChild() != true {
		t.Fatal("child session")
	}
	if (&Session{ID: "coord-1"}).IsWorkerChild() {
		t.Fatal("coordinator session")
	}
	var nilSess *Session
	if nilSess.IsWorkerChild() {
		t.Fatal("nil session")
	}
}
