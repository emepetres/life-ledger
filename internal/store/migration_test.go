package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/emepetres/life-ledger/internal/expense"
)

// A database created before the Shared tab existed keeps working: its split
// expenses become Shared expenses paid by Javi, its other rows stay ordinary, and
// the '*' marker is stripped from the retained raw text of the split ones.
func TestMigrationKeepsLegacyRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := fs.Sub(migrationsFS, "migrations")
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		t.Fatal(err)
	}
	// The schema as it was before this change: migrations 1 and 2 only.
	if _, err := provider.UpTo(ctx, 2); err != nil {
		t.Fatalf("legacy migrations: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO expense (date, amount, description, split, account, raw_text, created_at, updated_at)
		 VALUES ('2026-07-01', 5000, 'cena', 1, NULL, '50 cena * 1/7', '2026-07-01T10:00:00Z', '2026-07-01T10:00:00Z')`,
		`INSERT INTO expense (date, amount, description, split, account, raw_text, created_at, updated_at)
		 VALUES ('2026-07-02', 900, 'café', 0, NULL, '9 café', '2026-07-02T10:00:00Z', '2026-07-02T10:00:00Z')`,
		`INSERT INTO income (date, amount, description, account, raw_text, created_at, updated_at)
		 VALUES ('2026-07-03', 100, 'x', NULL, '+1 x', '2026-07-03T10:00:00Z', '2026-07-03T10:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seeding legacy row: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a legacy database: %v", err)
	}
	defer s.Close()

	list, err := s.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	cafe, cena := list[0], list[1] // newest first
	if cena.Description != "cena" || !cena.Shared || cena.PaidBy != expense.Javi || cena.Settlement {
		t.Errorf("legacy split expense: %+v", cena)
	}
	if cena.RawText != "50 cena 1/7" {
		t.Errorf("RawText = %q, want the '*' stripped: %q", cena.RawText, "50 cena 1/7")
	}
	if cafe.Shared || cafe.PaidBy != expense.Javi {
		t.Errorf("legacy ordinary expense: %+v", cafe)
	}
	incomes, _ := s.ListIncomes(ctx)
	if len(incomes) != 1 || incomes[0].Settlement {
		t.Errorf("legacy income: %+v", incomes)
	}
}
