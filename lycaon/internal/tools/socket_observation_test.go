package tools

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestSocketObservationFieldsProjectScopeAndState(t *testing.T) {
	durable := confine.SocketGrant{ApprovedPath: "/tmp/a.sock", ResolvedPath: "/private/tmp/a.sock"}
	chat := confine.SocketGrant{ApprovedPath: "/tmp/b.sock", ResolvedPath: "/private/tmp/b.sock"}
	requested := confine.SocketGrant{ApprovedPath: "/tmp/c.sock", ResolvedPath: "/private/tmp/c.sock"}
	grants := []confine.SocketGrant{durable, chat, requested}

	beforeApproval := socketObservationFields(grants, []confine.SocketGrant{requested}, []confine.SocketGrant{chat}, []confine.SocketGrant{durable}, nil)
	if !reflect.DeepEqual(beforeApproval.scopes, []string{"durable", "chat", "requested"}) ||
		!reflect.DeepEqual(beforeApproval.states, []string{"existing", "existing", "new"}) {
		t.Fatalf("before approval = %+v", beforeApproval)
	}

	afterApproval := socketObservationFields(grants, []confine.SocketGrant{requested}, []confine.SocketGrant{chat}, []confine.SocketGrant{durable}, []confine.SocketGrant{requested})
	if !reflect.DeepEqual(afterApproval.scopes, []string{"durable", "chat", "current_action"}) ||
		!reflect.DeepEqual(afterApproval.states, []string{"existing", "existing", "existing"}) {
		t.Fatalf("after approval = %+v", afterApproval)
	}
}
