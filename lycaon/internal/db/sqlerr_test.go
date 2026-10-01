package db_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
)

func TestIsNoRows(t *testing.T) {
	if !db.IsNoRows(sql.ErrNoRows) {
		t.Fatal("expected true for ErrNoRows")
	}
	if db.IsNoRows(fmt.Errorf("other")) {
		t.Fatal("expected false for unrelated error")
	}
	if !db.IsNoRows(fmt.Errorf("wrap: %w", sql.ErrNoRows)) {
		t.Fatal("expected true for wrapped ErrNoRows")
	}
}
