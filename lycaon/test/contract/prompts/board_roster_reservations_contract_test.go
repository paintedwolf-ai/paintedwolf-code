package contract

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoardWorkerRosterEntryReservationsField(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeOf(api.BoardWorkerRosterEntry{}).FieldByName("Reservations")
	if !ok {
		t.Fatal("BoardWorkerRosterEntry missing Reservations field")
	}
	if field.Type != reflect.TypeOf([]string{}) {
		t.Fatalf("Reservations type = %v want []string", field.Type)
	}
	if got := field.Tag.Get("json"); got != "reservations,omitempty" {
		t.Fatalf("Reservations json tag = %q", got)
	}
}
